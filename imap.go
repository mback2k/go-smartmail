/*
	go-smartmail - Folder-based smart actions for IMAP servers.
	Copyright (C) 2019  Marc Hoersken <info@marc-hoersken.de>

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package main

import (
	"context"
	"strings"

	joda "github.com/vjeantet/jodaTime"

	imap "github.com/emersion/go-imap"
	idle "github.com/emersion/go-imap-idle"
	move "github.com/emersion/go-imap-move"
	client "github.com/emersion/go-imap/client"

	log "github.com/sirupsen/logrus"
)

// SmartServer contains the IMAP credentials.
type SmartServer struct {
	Server   string
	Username string
	Password string
	Mailbox  string

	config   *smartConfig
	imapconn *client.Client
	idleconn *client.Client
	idle     *idle.Client
	updates  chan client.Update
}

// SmartActions defines the activities.
type SmartActions struct {
	Move         string
	MoveJodaTime bool
}

type smartState int

const (
	initialState    = (smartState)(0 << 0)
	connectingState = (smartState)(1 << 0)
	connectedState  = (smartState)(1 << 1)
	watchingState   = (smartState)(1 << 2)
	handlingState   = (smartState)(1 << 3)
	shutdownState   = (smartState)(1 << 4)
)

type smartConfig struct {
	Name         string
	SmartServer  `mapstructure:"IMAP"`
	SmartActions `mapstructure:"Actions"`

	state smartState
	total uint64
	err   error
}

func (c *SmartServer) open() (*client.Client, error) {
	con, err := client.DialTLS(c.Server, nil)
	if err != nil {
		return nil, err
	}
	err = con.Login(c.Username, c.Password)
	if err != nil {
		return nil, err
	}
	return con, nil
}

func (c *SmartServer) openIMAP() error {
	con, err := c.open()
	if err != nil {
		return err
	}
	c.imapconn = con
	return nil
}

func (c *SmartServer) openIDLE() error {
	con, err := c.open()
	if err != nil {
		return err
	}
	c.idleconn = con
	return nil
}

func (c *SmartServer) selectIMAP() (*client.MailboxUpdate, error) {
	status, err := c.imapconn.Select(c.Mailbox, false)
	update := &client.MailboxUpdate{Mailbox: status}
	return update, err
}

func (c *SmartServer) selectIDLE() (*client.MailboxUpdate, error) {
	status, err := c.idleconn.Select(c.Mailbox, true)
	update := &client.MailboxUpdate{Mailbox: status}
	return update, err
}

func (c *SmartServer) initIDLE() error {
	update, err := c.selectIDLE()
	if err != nil {
		return err
	}
	updates := make(chan client.Update, 1)
	updates <- update

	c.idle = idle.NewClient(c.idleconn)
	c.idleconn.Updates = updates
	c.updates = updates
	return nil
}

func (c *smartConfig) init() error {
	c.config = c
	c.state = connectingState
	err := c.openIMAP()
	if err != nil {
		return err
	}
	err = c.closeIMAP()
	if err != nil {
		return err
	}
	err = c.openIDLE()
	if err != nil {
		return err
	}
	err = c.initIDLE()
	if err != nil {
		return err
	}
	c.state = connectedState
	return err
}

func (c *SmartServer) closeIMAP() error {
	if c.imapconn == nil {
		return nil
	}
	err := c.imapconn.Logout()
	if err != nil {
		return err
	}
	c.imapconn = nil
	return nil
}

func (c *SmartServer) closeIDLE() error {
	if c.idleconn == nil {
		return nil
	}
	err := c.idleconn.Logout()
	if err != nil {
		return err
	}
	c.idleconn = nil
	return nil
}

func (c *smartConfig) close() error {
	c.state = shutdownState
	err := c.closeIDLE()
	if err != nil {
		return err
	}
	err = c.closeIMAP()
	if err != nil {
		return err
	}
	c.state = initialState
	return nil
}

func (c *smartConfig) watch(ctx context.Context) error {
	defer func(c *smartConfig, s smartState) {
		c.state = s
	}(c, c.state)
	c.state = watchingState

	c.log().Info("Begin idling")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errors := make(chan error, 1)
	go func() {
		errors <- c.idle.IdleWithFallback(ctx.Done(), 0)
	}()
	for {
		select {
		case update := <-c.updates:
			c.log().Infof("New update: %#v", update)
			_, ok := update.(*client.MailboxUpdate)
			if ok {
				c.handle(cancel)
			}
		case err := <-errors:
			c.log().Warn("Not idling anymore: ", err)
			return err
		}
	}
}

func (c *smartConfig) handle(cancel context.CancelFunc) {
	defer func(c *smartConfig, s smartState) {
		c.state = s
	}(c, c.state)
	c.state = handlingState

	c.log().Info("Begin handling")

	err := c.openIMAP()
	if err != nil {
		c.log().Warn("Source connection failed: ", err)
		cancel()
		return
	}
	defer c.closeIMAP()

	errors := make(chan error, 1)
	messages := make(chan *imap.Message, 100)

	go c.fetchMessages(messages, errors)
	go c.smartMessages(messages, errors)

	for {
		err, more := <-errors
		if err != nil {
			c.log().Warn("Message handling failed: ", err)
			cancel()
		}
		if !more {
			c.log().Info("Message handling finished")
			break
		}
	}
}

func (c *SmartServer) fetchMessages(messages chan *imap.Message, errors chan<- error) {
	update, err := c.selectIMAP()
	if err != nil {
		errors <- err
		close(messages)
		return
	}

	if update.Mailbox.Messages < 1 {
		close(messages)
		return
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, update.Mailbox.Messages)

	errors <- c.imapconn.Fetch(seqset, []imap.FetchItem{
		"UID", "FLAGS", "INTERNALDATE"}, messages)
}

func (c *SmartServer) smartMessages(messages <-chan *imap.Message, errors chan<- error) {
	defer close(errors)

	move := move.NewClient(c.imapconn)
	for msg := range messages {
		c.config.log().Info("Handling message: ", msg.Uid)

		deleted := false
		for _, flag := range msg.Flags {
			switch flag {
			case imap.DeletedFlag:
				deleted = true
				break
			}
		}
		if deleted {
			c.config.log().Info("Ignoring message: ", msg.Uid)
			continue
		}

		if c.config.SmartActions.Move != "" {
			seqset := new(imap.SeqSet)
			seqset.AddNum(msg.Uid)

			var mailbox string
			if c.config.SmartActions.MoveJodaTime {
				fields := strings.Split(c.config.SmartActions.Move, "%")
				for i, field := range fields {
					if i%2 == 1 {
						field = joda.Format(field, msg.InternalDate)
					}
					mailbox = mailbox + field
				}
			} else {
				mailbox = c.config.SmartActions.Move
			}

			c.config.log().Info("Moving message: ", msg.Uid, " to ", mailbox)

			err := move.UidMoveWithFallback(seqset, mailbox)
			if err != nil {
				errors <- err
				return
			}

			c.config.total++
		}
	}
}

func (c *smartConfig) run(ctx context.Context, done chan<- *smartConfig) {
	defer c.done(done)
	c.err = c.init()
	if c.err == nil {
		c.err = c.watch(ctx)
	}
}

func (c *smartConfig) done(done chan<- *smartConfig) {
	err := c.close()
	if c.err == nil {
		c.err = err
	}
	done <- c
}

func (c *smartConfig) log() *log.Entry {
	return log.WithFields(log.Fields{
		"name":  c.Name,
		"state": c.state,
	})
}

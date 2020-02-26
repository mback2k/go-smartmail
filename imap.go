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
	ctx   context.Context
}

func (s *SmartServer) open() (*client.Client, error) {
	con, err := client.DialTLS(s.Server, nil)
	if err != nil {
		return nil, err
	}
	err = con.Login(s.Username, s.Password)
	if err != nil {
		return nil, err
	}
	return con, nil
}

func (s *SmartServer) openIMAP() error {
	con, err := s.open()
	if err != nil {
		return err
	}
	s.imapconn = con
	return nil
}

func (s *SmartServer) openIDLE() error {
	con, err := s.open()
	if err != nil {
		return err
	}
	s.idleconn = con
	return nil
}

func (s *SmartServer) selectIMAP() (*client.MailboxUpdate, error) {
	status, err := s.imapconn.Select(s.Mailbox, false)
	update := &client.MailboxUpdate{Mailbox: status}
	return update, err
}

func (s *SmartServer) selectIDLE() (*client.MailboxUpdate, error) {
	status, err := s.idleconn.Select(s.Mailbox, true)
	update := &client.MailboxUpdate{Mailbox: status}
	return update, err
}

func (s *SmartServer) initIDLE() error {
	update, err := s.selectIDLE()
	if err != nil {
		return err
	}
	updates := make(chan client.Update, 1)
	updates <- update

	s.idle = idle.NewClient(s.idleconn)
	s.idleconn.Updates = updates
	s.updates = updates
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

func (s *SmartServer) closeIMAP() error {
	if s.imapconn == nil {
		return nil
	}
	err := s.imapconn.Logout()
	if err != nil {
		return err
	}
	s.imapconn = nil
	return nil
}

func (s *SmartServer) closeIDLE() error {
	if s.idleconn == nil {
		return nil
	}
	err := s.idleconn.Logout()
	if err != nil {
		return err
	}
	s.idleconn = nil
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

func (c *smartConfig) watch() error {
	defer func(c *smartConfig, s smartState) {
		c.state = s
	}(c, c.state)
	c.state = watchingState

	c.log().Info("Begin idling")

	ctx, cancel := context.WithCancel(c.ctx)
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
			c.log().Warnf("Not idling anymore: %v", err)
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
		c.log().Warnf("Source connection failed: %v", err)
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
			c.log().Warnf("Message handling failed: %v", err)
			cancel()
		}
		if !more {
			c.log().Info("Message handling finished")
			break
		}
	}
}

func (s *SmartServer) fetchMessages(messages chan *imap.Message, errors chan<- error) {
	update, err := s.selectIMAP()
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

	errors <- s.imapconn.Fetch(seqset, []imap.FetchItem{
		"UID", "FLAGS", "INTERNALDATE"}, messages)
}

func (s *SmartServer) smartMessages(messages <-chan *imap.Message, errors chan<- error) {
	defer close(errors)

	move := move.NewClient(s.imapconn)
	for msg := range messages {
		s.config.log().Infof("Handling message: %d", msg.Uid)

		deleted := false
		for _, flag := range msg.Flags {
			switch flag {
			case imap.DeletedFlag:
				deleted = true
				break
			}
		}
		if deleted {
			s.config.log().Infof("Ignoring message: %d", msg.Uid)
			continue
		}

		if s.config.SmartActions.Move != "" {
			seqset := new(imap.SeqSet)
			seqset.AddNum(msg.Uid)

			var mailbox string
			if s.config.SmartActions.MoveJodaTime {
				fields := strings.Split(s.config.SmartActions.Move, "%")
				for i, field := range fields {
					if i%2 == 1 {
						field = joda.Format(field, msg.InternalDate)
					}
					mailbox = mailbox + field
				}
			} else {
				mailbox = s.config.SmartActions.Move
			}

			s.config.log().Infof("Moving message: %d to '%s'", msg.Uid, mailbox)

			err := move.UidMoveWithFallback(seqset, mailbox)
			if err != nil {
				errors <- err
				return
			}

			s.config.total++
		}
	}
}

func (c *smartConfig) run() error {
	err := c.init()
	if err != nil {
		return err
	}
	defer c.close()
	return c.watch()
}

func (c *smartConfig) log() *log.Entry {
	return log.WithFields(log.Fields{
		"name":  c.Name,
		"state": c.state,
	})
}

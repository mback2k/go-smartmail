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
	"log"
	"net/http"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rollbar/rollbar-go"
)

func reportError() {
	if r := recover(); r != nil {
		rollbar.Critical(r)
		rollbar.Wait()
		log.Fatal(r)
	}
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	if config.Rollbar != nil {
		rollbar.SetToken(config.Rollbar.AccessToken)
		rollbar.SetEnvironment(config.Rollbar.Environment)
		defer reportError()
		log.Println("Errors will be reported to rollbar.com!")
	}

	if config.Metrics != nil && config.Metrics.ListenAddress != "" {
		cc := NewCollector(config)
		prometheus.MustRegister(cc)
		http.Handle("/metrics", promhttp.Handler())
		go http.ListenAndServe(config.Metrics.ListenAddress, nil)
	}

	runtime.GC()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan *smartConfig, 1)
	for _, c := range config.Accounts {
		log.Println(c.Name, "[", c.state, "]:", c.Server)
		go c.run(ctx, done)
	}
	for range config.Accounts {
		c := <-done
		if c.err != nil {
			cancel()
			log.Println(c.Name, "[", c.state, "]:", c.err)
		}
	}
}

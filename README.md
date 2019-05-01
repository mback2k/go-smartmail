go-smartmail
==========
This Go program is a simple tool to provide folder-based smart actions for IMAP servers.

[![Build Status](https://travis-ci.org/mback2k/go-smartmail.svg?branch=master)](https://travis-ci.org/mback2k/go-smartmail)
[![GoDoc](https://godoc.org/github.com/mback2k/go-smartmail?status.svg)](https://godoc.org/github.com/mback2k/go-smartmail)
[![Go Report Card](https://goreportcard.com/badge/github.com/mback2k/go-smartmail)](https://goreportcard.com/report/github.com/mback2k/go-smartmail)

Dependencies
------------
Special thanks to [@emersion](https://github.com/emersion) for creating and providing
the following Go libraries that are the main building blocks of this program:

- https://github.com/emersion/go-imap
- https://github.com/emersion/go-imap-idle
- https://github.com/emersion/go-imap-move

Additional dependencies are the following awesome Go libraries:

- https://github.com/spf13/viper

Installation
------------
You basically have two options to install this Go program package:

1. If you have Go installed and configured on your PATH, just do the following go get inside your GOPATH to get the latest version:

```
go get -u github.com/mback2k/go-smartmail
```

2. If you do not have Go installed and just want to use a released binary,
then you can just go ahead and download a pre-compiled Linux amd64 binary from the [Github releases](https://github.com/mback2k/go-smartmail/releases).

Finally put the go-smartmail binary onto your PATH and make sure it is executable.

Configuration
-------------
The following YAML file is an example configuration with one transfer to be handled:

```
Accounts:

- Name: Archive per year
  IMAP:
    Server: imap-source.example.com:993
    Username: your-imap-source-username
    Password: your-imap-source-username
    Mailbox: INBOX.Archive
  Actions:
    Move: INBOX.Archive.%YYYY%
    MoveJodaTime: true
```

You can have multiple accounts handled by repeating the `- Name: ...` section.

Save this file in one of the following locations and run `./go-smartmail`:

- /etc/go-smartmail/go-smartmail.yaml
- $HOME/.go-smartmail.yaml
- $PWD/go-smartmail.yaml

License
-------
Copyright (C) 2019  Marc Hoersken <info@marc-hoersken.de>

This software is licensed as described in the file LICENSE, which
you should have received as part of this software distribution.

All trademarks are the property of their respective owners.

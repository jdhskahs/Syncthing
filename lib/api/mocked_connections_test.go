// Copyright (C) 2016 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package api

type mockedConnections struct{}

func (m *mockedConnections) Status() map[string]interface{} {
	return nil
}

func (m *mockedConnections) NATType() string {
	return ""
}

func (m *mockedConnections) Serve() {}

func (m *mockedConnections) Stop() {}

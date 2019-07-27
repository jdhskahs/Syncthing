// Copyright (C) 2015 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package syncthing

import (
	"fmt"

	"github.com/thejerf/suture"

	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/util"
)

// The verbose logging service subscribes to events and prints these in
// verbose format to the console using INFO level.
type verboseService struct {
	suture.Service
	sub *events.Subscription
}

func newVerboseService() *verboseService {
	s := &verboseService{
		sub: events.Default.Subscribe(events.AllEvents),
	}
	s.Service = util.AsService(s.serve)
	return s
}

// serve runs the verbose logging service.
func (s *verboseService) serve(stop chan struct{}) {
	for {
		select {
		case ev := <-s.sub.C():
			formatted := s.formatEvent(ev)
			if formatted != "" {
				l.Verboseln(formatted)
			}
		case <-stop:
			return
		}
	}
}

// Stop stops the verbose logging service.
func (s *verboseService) Stop() {
	s.Service.Stop()
	events.Default.Unsubscribe(s.sub)

}

func (s *verboseService) formatEvent(ev events.Event) string {
	switch ev.Type {
	case events.DownloadProgress, events.LocalIndexUpdated:
		// Skip
		return ""

	case events.Starting:
		return fmt.Sprintf("Starting up (%s)", ev.Data.(map[string]string)["home"])

	case events.StartupComplete:
		return "Startup complete"

	case events.DeviceDiscovered:
		data := ev.Data.(map[string]interface{})
		return fmt.Sprintf("Discovered device %v at %v", data["device"], data["addrs"])

	case events.DeviceConnected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Connected to device %v at %v (type %s)", data["id"], data["addr"], data["type"])

	case events.DeviceDisconnected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Disconnected from device %v", data["id"])

	case events.StateChanged:
		data := ev.Data.(map[string]interface{})
		return fmt.Sprintf("Folder %q is now %v", data["folder"], data["to"])

	case events.LocalChangeDetected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Local change detected in folder %q: %s %s %s", data["folder"], data["action"], data["type"], data["path"])

	case events.RemoteChangeDetected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Remote change detected in folder %q: %s %s %s", data["folder"], data["action"], data["type"], data["path"])

	case events.RemoteIndexUpdated:
		data := ev.Data.(map[string]interface{})
		return fmt.Sprintf("Device %v sent an index update for %q with %d items", data["device"], data["folder"], data["items"])

	case events.DeviceRejected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Rejected connection from device %v at %v", data["device"], data["address"])

	case events.FolderRejected:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Rejected unshared folder %q from device %v", data["folder"], data["device"])

	case events.ItemStarted:
		data := ev.Data.(map[string]string)
		return fmt.Sprintf("Started syncing %q / %q (%v %v)", data["folder"], data["item"], data["action"], data["type"])

	case events.ItemFinished:
		data := ev.Data.(map[string]interface{})
		if err, ok := data["error"].(*string); ok && err != nil {
			// If the err interface{} is not nil, it is a string pointer.
			// Dereference it to get the actual error or Sprintf will print
			// the pointer value....
			return fmt.Sprintf("Finished syncing %q / %q (%v %v): %v", data["folder"], data["item"], data["action"], data["type"], *err)
		}
		return fmt.Sprintf("Finished syncing %q / %q (%v %v): Success", data["folder"], data["item"], data["action"], data["type"])

	case events.ConfigSaved:
		return "Configuration was saved"

	case events.FolderCompletion:
		data := ev.Data.(map[string]interface{})
		return fmt.Sprintf("Completion for folder %q on device %v is %v%%", data["folder"], data["device"], data["completion"])

	case events.FolderSummary:
		data := ev.Data.(map[string]interface{})
		sum := make(map[string]interface{})
		for k, v := range data["summary"].(map[string]interface{}) {
			if k == "invalid" || k == "ignorePatterns" || k == "stateChanged" {
				continue
			}
			sum[k] = v
		}
		return fmt.Sprintf("Summary for folder %q is %v", data["folder"], sum)

	case events.FolderScanProgress:
		data := ev.Data.(map[string]interface{})
		folder := data["folder"].(string)
		current := data["current"].(int64)
		total := data["total"].(int64)
		rate := data["rate"].(float64) / 1024 / 1024
		var pct int64
		if total > 0 {
			pct = 100 * current / total
		}
		return fmt.Sprintf("Scanning folder %q, %d%% done (%.01f MiB/s)", folder, pct, rate)

	case events.DevicePaused:
		data := ev.Data.(map[string]string)
		device := data["device"]
		return fmt.Sprintf("Device %v was paused", device)

	case events.DeviceResumed:
		data := ev.Data.(map[string]string)
		device := data["device"]
		return fmt.Sprintf("Device %v was resumed", device)

	case events.FolderPaused:
		data := ev.Data.(map[string]string)
		id := data["id"]
		label := data["label"]
		return fmt.Sprintf("Folder %v (%v) was paused", id, label)

	case events.FolderResumed:
		data := ev.Data.(map[string]string)
		id := data["id"]
		label := data["label"]
		return fmt.Sprintf("Folder %v (%v) was resumed", id, label)

	case events.ListenAddressesChanged:
		data := ev.Data.(map[string]interface{})
		address := data["address"]
		lan := data["lan"]
		wan := data["wan"]
		return fmt.Sprintf("Listen address %s resolution has changed: lan addresses: %s wan addresses: %s", address, lan, wan)

	case events.LoginAttempt:
		data := ev.Data.(map[string]interface{})
		username := data["username"].(string)
		var success string
		if data["success"].(bool) {
			success = "successful"
		} else {
			success = "failed"
		}
		return fmt.Sprintf("Login %s for username %s.", success, username)
	}

	return fmt.Sprintf("%s %#v", ev.Type, ev)
}

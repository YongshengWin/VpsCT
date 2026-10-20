package openrc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"ctlvps/internal/secureupdate"
)

const agentReadyFile = "/run/ctlvps-openrc/agent-ready.json"

type agentReady struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// MarkAgentReady seals the successful startup refresh to this process lifetime.
// The installer releases its configuration lock before polling AgentReady.
func MarkAgentReady() error {
	pid, err := MainPID("ctlvps-agent.service")
	if err != nil {
		return err
	}
	if pid != os.Getpid() {
		return errors.New("OpenRC startup is not the fixed agent main process")
	}
	start, err := processStart(pid)
	if err != nil {
		return err
	}
	b, err := json.Marshal(agentReady{PID: pid, Start: start})
	if err == nil {
		_, err = writeProtected(agentReadyFile, b, 0600)
	}
	return err
}

// AgentReady is a fixed, read-only local check. Supervisor status alone can
// succeed while Run is blocked on a configuration lock or retrying startup.
func AgentReady() error {
	if !Enabled() {
		return errors.New("OpenRC agent is unavailable")
	}
	b, err := secureupdate.ReadProtectedConfig(agentReadyFile)
	if err != nil {
		return err
	}
	var ready agentReady
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&ready); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF || ready.PID <= 0 || ready.Start == "" {
		return errors.New("invalid OpenRC agent startup receipt")
	}
	pid, err := MainPID("ctlvps-agent.service")
	if err != nil {
		return err
	}
	if pid != ready.PID {
		return errors.New("OpenRC agent has not completed startup")
	}
	start, err := processStart(pid)
	if err != nil {
		return err
	}
	if start != ready.Start {
		return errors.New("OpenRC startup receipt belongs to an earlier process")
	}
	if _, err := os.Lstat(refreshFile); !os.IsNotExist(err) {
		return errors.New("OpenRC helper refresh is incomplete")
	}
	for _, unit := range []string{"ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service"} {
		if unit != "ctlvps-agent.service" {
			if _, err := os.Lstat(filepath.Join(UnitDir, unit)); err == nil {
				if err := Verify(unit); err != nil {
					return err
				}
				pid, err := MainPID(unit)
				if err != nil {
					return err
				}
				if pid == 0 {
					return errors.New("installed OpenRC helper is not running")
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		current, err := helperCurrent(unit)
		if err != nil {
			return err
		}
		if !current {
			return errors.New("OpenRC service still runs the previous agent executable")
		}
	}
	return nil
}

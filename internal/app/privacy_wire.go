package app

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ApplyPrivacyPosture enforces the resolved privacy posture for one run. It is
// the app-layer bridge that makes the security.PrivacyMode helpers reachable
// from production code: config.Config.PrivacyMode is decoded from the
// privacyMode / privacy_mode keys, ARGENT_PRIVACY_MODE, and --privacy-mode, and
// this function turns that posture into concrete decisions.
//
// It returns the subset of webToolNames that must be withheld from the model
// when the mode is local-only. An empty mode is treated as the default "any"
// posture (a no-op). A remote baseURL in local-only mode, or an unknown mode,
// is an error so a mistake cannot silently widen the posture.
//
// WIRING NOTE (for the owner of app_execute.go): call this once where tools are
// filtered, i.e. alongside the FilterSpecs closure in Execute, using the
// already-resolved values:
//
//	blocked, err := ApplyPrivacyPosture(res.Cfg.PrivacyMode, res.BaseURL, webToolNames)
//	if err != nil {
//	    return err
//	}
//	// then drop `blocked` from the advertised tool specs before returning them.
//
// The base URL check alone is not enough: web_search / web_fetch can reach the
// network independently of the chat endpoint, so both decisions belong here.
func ApplyPrivacyPosture(mode string, baseURL string, webToolNames []string) (blockedWebTools []string, err error) {
	privacyMode := security.PrivacyMode(strings.ToLower(strings.TrimSpace(mode)))
	if privacyMode == "" {
		privacyMode = security.PrivacyAny
	}

	allowed, reason := security.AllowRemote(privacyMode, baseURL, false)
	if !allowed {
		return nil, fmt.Errorf("app: privacy: base URL denied: %s", reason)
	}

	if security.ShouldBlockWebTool(privacyMode) {
		blockedWebTools = append([]string(nil), webToolNames...)
	}
	return blockedWebTools, nil
}

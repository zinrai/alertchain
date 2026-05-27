// convert.go translates between alertchain internal types and the
// Alertmanager v2 alert wire format.
//
// alertchain consumes one Alertmanager v2 endpoint (POST /api/v2/alerts)
// to remain compatible with Prometheus, vmalert, and promxy. The
// conversion is one-directional: incoming PostableAlerts are translated
// into the internal Alert type. The mute API and all other endpoints
// are alertchain's own and do not go through this layer.
package main

import (
	"time"

	ammodels "github.com/prometheus/alertmanager/api/v2/models"
)

// alertFromPostable converts an Alertmanager v2 PostableAlert (as
// received from Prometheus or vmalert) into the alertchain internal
// Alert representation.
func alertFromPostable(p *ammodels.PostableAlert) *Alert {
	a := &Alert{
		Labels:       map[string]string(p.Labels),
		Annotations:  map[string]string(p.Annotations),
		StartsAt:     time.Time(p.StartsAt),
		EndsAt:       time.Time(p.EndsAt),
		GeneratorURL: string(p.Alert.GeneratorURL),
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	return a
}

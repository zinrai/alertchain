// convert.go translates Alertmanager v2 PostableAlerts into the
// alertchain internal Alert type. One-directional.
package alertchain

import (
	"time"

	ammodels "github.com/prometheus/alertmanager/api/v2/models"
)

// AlertFromPostable converts an Alertmanager v2 PostableAlert (as
// received from Prometheus or vmalert) into the alertchain internal
// Alert representation.
func AlertFromPostable(p *ammodels.PostableAlert) *Alert {
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

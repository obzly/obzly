package alerting

import (
	"context"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// activityDays: the days the Rules tab shows, today included.
	activityDays = 7
	// keepActivity: daily counters older than this are pruned with the outbox.
	keepActivity = 14 * 24 * time.Hour
	// activityMigration is the migration that started counting: days before it are unknown.
	activityMigration = 20261118110000
	dayLayout         = "2006-01-02"
)

// RuleActivityResponse is each rule's firings and notifications per UTC day.
type RuleActivityResponse struct {
	// Days are the UTC days counted, oldest first; the last is today so far.
	Days []string `json:"days"`
	// TrackedSince is when this Site started counting (day precision); earlier days are
	// unknown, as are days before a rule was created. Nil when it can't be told.
	TrackedSince *time.Time     `json:"trackedSince"`
	Rules        []RuleActivity `json:"rules"`
}

// RuleActivity is one rule's counts, one per day of Days. Fired: series that went from
// pending to firing; Notifications: deliveries to its channels (firing, reminders and
// resolved; silenced ones aren't delivered).
type RuleActivity struct {
	RuleID             uuid.UUID `json:"ruleId"`
	Fired              []int     `json:"fired"`
	Notifications      []int     `json:"notifications"`
	FiredTotal         int       `json:"firedTotal"`
	NotificationsTotal int       `json:"notificationsTotal"`
}

// AlertsSummary is the Alerts page header: alerts firing and pending now, rules, and
// silences current or upcoming (as the Silences tab lists them).
type AlertsSummary struct {
	Firing   int64 `json:"firing"`
	Pending  int64 `json:"pending"`
	Rules    int64 `json:"rules"`
	Silences int64 `json:"silences"`
}

// RuleActivity is the project's rules' activity over the last activityDays UTC days.
func (s *Service) RuleActivity(projectID uuid.UUID) (*RuleActivityResponse, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(activityDays - 1))
	response := &RuleActivityResponse{Days: make([]string, activityDays), Rules: []RuleActivity{}}
	index := make(map[string]int, activityDays)
	for i := range activityDays {
		day := first.AddDate(0, 0, i).Format(dayLayout)
		response.Days[i] = day
		index[day] = i
	}
	rows, err := s.repo.activity(projectID, first)
	if err != nil {
		return nil, err
	}
	byRule := map[uuid.UUID]int{}
	for _, row := range rows {
		at, ok := byRule[row.RuleID]
		if !ok {
			at = len(response.Rules)
			byRule[row.RuleID] = at
			response.Rules = append(response.Rules, RuleActivity{
				RuleID: row.RuleID, Fired: make([]int, activityDays), Notifications: make([]int, activityDays),
			})
		}
		if row.Day == nil {
			continue
		}
		i, ok := index[*row.Day]
		if !ok {
			continue
		}
		rule := &response.Rules[at]
		rule.Fired[i] += row.Fired
		rule.Notifications[i] += row.Notifications
		rule.FiredTotal += row.Fired
		rule.NotificationsTotal += row.Notifications
	}
	response.TrackedSince, err = s.repo.activityTrackedSince()
	return response, err
}

// Summary is the Alerts page header's counts.
func (s *Service) Summary(projectID uuid.UUID) (*AlertsSummary, error) {
	return s.repo.summary(projectID, time.Now().UTC())
}

type activityRow struct {
	RuleID        uuid.UUID
	Day           *string
	Fired         int
	Notifications int
}

// activity is every rule of the project with its counter rows since first (a rule with
// none has one row with a nil Day). Rules by name, then days.
func (r *repository) activity(projectID uuid.UUID, first time.Time) ([]activityRow, error) {
	var rows []activityRow
	err := storage.GetDb().Raw(`
		SELECT r.id AS rule_id, to_char(a.day, 'YYYY-MM-DD') AS day, COALESCE(a.fired, 0) AS fired,
		       COALESCE(a.notifications, 0) AS notifications
		FROM alert_rules r
		LEFT JOIN alert_rule_activity a ON a.rule_id = r.id AND a.day >= ?::date
		WHERE r.project_id = ?
		ORDER BY r.name, r.id, a.day`, first.Format(dayLayout), projectID).Scan(&rows).Error
	return rows, err
}

func (r *repository) activityTrackedSince() (*time.Time, error) {
	var since []time.Time
	err := storage.GetDb().Raw(`SELECT tstamp FROM goose_db_version WHERE version_id = ? AND is_applied
		ORDER BY tstamp LIMIT 1`, activityMigration).Scan(&since).Error
	if err != nil || len(since) == 0 {
		// Tests and Sites migrated some other way have no record: unknown, not an error.
		return nil, nil
	}
	tracked := since[0].UTC()
	return &tracked, nil
}

func (r *repository) summary(projectID uuid.UUID, now time.Time) (*AlertsSummary, error) {
	var summary AlertsSummary
	err := storage.GetDb().Raw(`
		SELECT
			(SELECT count(*) FROM alert_states s JOIN alert_rules r ON r.id = s.rule_id
			  WHERE r.project_id = ? AND s.state = ?) AS firing,
			(SELECT count(*) FROM alert_states s JOIN alert_rules r ON r.id = s.rule_id
			  WHERE r.project_id = ? AND s.state = ?) AS pending,
			(SELECT count(*) FROM alert_rules WHERE project_id = ?) AS rules,
			(SELECT count(*) FROM silences WHERE project_id = ? AND ends_at > ?) AS silences`,
		projectID, StateFiring, projectID, StatePending, projectID, projectID, now).Scan(&summary).Error
	return &summary, err
}

// countActivity adds to a rule's counters for at's UTC day, inside tx.
func countActivity(tx *gorm.DB, ruleID uuid.UUID, at time.Time, fired, notifications int) error {
	return tx.Exec(`
		INSERT INTO alert_rule_activity (rule_id, day, fired, notifications) VALUES (?, ?::date, ?, ?)
		ON CONFLICT (rule_id, day) DO UPDATE SET
			fired = alert_rule_activity.fired + EXCLUDED.fired,
			notifications = alert_rule_activity.notifications + EXCLUDED.notifications`,
		ruleID, at.UTC().Format(dayLayout), fired, notifications).Error
}

// countDelivered counts one delivered notification. The rule may have been deleted since
// it was queued; then there is nothing to count.
func (r *repository) countDelivered(ruleID uuid.UUID, at time.Time) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var exists bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM alert_rules WHERE id = ?)", ruleID).
			Scan(&exists).Error; err != nil || !exists {
			return err
		}
		return countActivity(tx, ruleID, at, 0, 1)
	})
}

func (r *repository) pruneActivity(ctx context.Context, before time.Time) error {
	return storage.GetDb().WithContext(ctx).
		Exec("DELETE FROM alert_rule_activity WHERE day < ?::date", before.UTC().Format(dayLayout)).Error
}

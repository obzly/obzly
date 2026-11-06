package synthetics

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/storage"
)

type repository struct{}

// Probes

func (r *repository) probes() ([]*Probe, error) {
	var probes []*Probe
	err := storage.GetDb().Order("lower(name)").Find(&probes).Error
	return probes, err
}

func (r *repository) probe(id uuid.UUID) (*Probe, error) {
	var probe Probe
	if err := storage.GetDb().Where("id = ?", id).First(&probe).Error; err != nil {
		return nil, err
	}
	return &probe, nil
}

func (r *repository) createProbe(probe *Probe, token *ProbeToken) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(probe).Error; err != nil {
			return err
		}
		return tx.Create(token).Error
	})
}

// replaceToken stores token and lets the current ones run out at graceUntil so running members
// can move over (revoke stops them now). It returns the revoked tokens' hashes.
func (r *repository) replaceToken(
	probeID uuid.UUID, token *ProbeToken, graceUntil time.Time, revoke bool,
) ([]string, error) {
	var revoked []string
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if revoke {
			current := tx.Model(&ProbeToken{}).Where("probe_id = ? AND revoked_at IS NULL", probeID)
			if err := current.Pluck("token_hash", &revoked).Error; err != nil {
				return err
			}
			if err := tx.Model(&ProbeToken{}).Where("probe_id = ? AND revoked_at IS NULL", probeID).
				Update("revoked_at", token.CreatedAt).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&ProbeToken{}).
			Where("probe_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", probeID, graceUntil).
			Update("expires_at", graceUntil).Error; err != nil {
			return err
		}
		return tx.Create(token).Error
	})
	return revoked, err
}

// touchMember records an instance as alive. A new one is added only while the group has fewer
// than limit instances seen since; false means it was refused.
func (r *repository) touchMember(member *ProbeMember, limit int, since time.Time) (bool, error) {
	db := storage.GetDb()
	updated := db.Exec(`UPDATE synthetic_probe_members SET version = ?, checks = ?, last_seen_at = ?
		WHERE probe_id = ? AND name = ?`, member.Version, member.Checks, member.LastSeenAt, member.ProbeID, member.Name)
	if updated.Error != nil || updated.RowsAffected > 0 {
		return updated.RowsAffected > 0, updated.Error
	}
	inserted := db.Exec(`INSERT INTO synthetic_probe_members (probe_id, name, version, checks, first_seen, last_seen_at)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE (SELECT count(*) FROM synthetic_probe_members WHERE probe_id = ? AND last_seen_at >= ?) < ?
		ON CONFLICT (probe_id, name) DO UPDATE SET version = EXCLUDED.version, checks = EXCLUDED.checks,
			last_seen_at = EXCLUDED.last_seen_at`,
		member.ProbeID, member.Name, member.Version, member.Checks, member.LastSeenAt, member.LastSeenAt,
		member.ProbeID, since, limit)
	return inserted.RowsAffected > 0, inserted.Error
}

// forgetMembers deletes the group's instances not seen since.
func (r *repository) forgetMembers(probeID uuid.UUID, since time.Time) error {
	return storage.GetDb().Where("probe_id = ? AND last_seen_at < ?", probeID, since).Delete(&ProbeMember{}).Error
}

// configVersion changes whenever a monitor, destination or cloud connection is created, changed
// or deleted (each bumps a version or an updated_at).
func (r *repository) configVersion() (string, error) {
	var marker string
	err := storage.GetDb().Raw(`SELECT concat_ws('|',
		(SELECT count(*) || ':' || coalesce(sum(version), 0) || ':' || coalesce(max(updated_at)::text, '')
			FROM synthetic_checks),
		(SELECT count(*) || ':' || coalesce(max(updated_at)::text, '') FROM synthetic_destinations),
		(SELECT count(*) || ':' || coalesce(sum(version), 0) || ':' || coalesce(max(created_at)::text, '')
			FROM cloud_connections))`).Scan(&marker).Error
	return marker, err
}

// members is every group's instances heard from since, newest first.
func (r *repository) members(since time.Time) ([]*ProbeMember, error) {
	var members []*ProbeMember
	err := storage.GetDb().Where("last_seen_at >= ?", since).Order("name").Find(&members).Error
	return members, err
}

// leaveMember backdates an instance's last poll to lastSeen.
func (r *repository) leaveMember(probeID uuid.UUID, name string, lastSeen time.Time) error {
	return storage.GetDb().Model(&ProbeMember{}).Where("probe_id = ? AND name = ?", probeID, name).
		Update("last_seen_at", lastSeen).Error
}

func (r *repository) liveMembers(probeID uuid.UUID, since time.Time) ([]string, error) {
	var names []string
	err := storage.GetDb().Model(&ProbeMember{}).Where("probe_id = ? AND last_seen_at >= ?", probeID, since).
		Order("name").Pluck("name", &names).Error
	return names, err
}

func (r *repository) deleteProbe(id uuid.UUID) ([]string, error) {
	var hashes []string
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ProbeToken{}).Where("probe_id = ?", id).Pluck("token_hash", &hashes).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&Probe{}).Error
	})
	return hashes, err
}

// probeByToken is the probe an active token belongs to, or nil.
func (r *repository) probeByToken(tokenHash string) (*Probe, error) {
	var probe Probe
	err := storage.GetDb().
		Joins("JOIN synthetic_probe_tokens t ON t.probe_id = synthetic_probes.id").
		Where("t.token_hash = ? AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > NOW())", tokenHash).
		First(&probe).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &probe, err
}

func (r *repository) touchProbe(id uuid.UUID, version string, at time.Time) error {
	return storage.GetDb().Model(&Probe{}).Where("id = ?", id).
		Updates(map[string]any{"last_seen_at": at, "version": version}).Error
}

// Destinations

func (r *repository) destinations() ([]*Destination, error) {
	var destinations []*Destination
	err := storage.GetDb().Order("lower(name)").Find(&destinations).Error
	return destinations, err
}

func (r *repository) destination(id uuid.UUID) (*Destination, error) {
	var destination Destination
	if err := storage.GetDb().Where("id = ?", id).First(&destination).Error; err != nil {
		return nil, err
	}
	return &destination, nil
}

func (r *repository) saveDestination(destination *Destination) error {
	return storage.GetDb().Save(destination).Error
}

func (r *repository) deleteDestination(id uuid.UUID) error {
	return storage.GetDb().Where("id = ?", id).Delete(&Destination{}).Error
}

func (r *repository) checksUsingDestination(id uuid.UUID) (int64, error) {
	var count int64
	err := storage.GetDb().Model(&Check{}).Where("destination_id = ?", id).Count(&count).Error
	return count, err
}

// Checks

func (r *repository) checks() ([]*Check, error) {
	var checks []*Check
	err := storage.GetDb().Order("lower(name)").Find(&checks).Error
	return checks, err
}

func (r *repository) enabledChecks() ([]*Check, error) {
	var checks []*Check
	err := storage.GetDb().Where("enabled").Order("id").Find(&checks).Error
	return checks, err
}

func (r *repository) check(id uuid.UUID) (*Check, error) {
	var check Check
	if err := storage.GetDb().Where("id = ?", id).First(&check).Error; err != nil {
		return nil, err
	}
	return &check, nil
}

func (r *repository) checkByName(name string) (*Check, error) {
	var check Check
	err := storage.GetDb().Where("lower(name) = ?", strings.ToLower(name)).First(&check).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &check, err
}

func (r *repository) createCheck(check *Check) error {
	return storage.GetDb().Create(check).Error
}

// updateCheck writes check if its stored version is still expected, and
// bumps the version; false means someone saved in between.
func (r *repository) updateCheck(check *Check, expected int) (bool, error) {
	check.Version = expected + 1
	result := storage.GetDb().Model(&Check{}).Where("id = ? AND version = ?", check.ID, expected).
		Select("*").Omit("id", "created_at", "created_by").Updates(check)
	return result.RowsAffected == 1, result.Error
}

// deleteCheck removes a monitor, and it from its children's parents.
func (r *repository) deleteCheck(id uuid.UUID) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := forgetParents(tx, []uuid.UUID{id}); err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&Check{}).Error
	})
}

// forgetParents removes ids from every monitor's parents in one statement.
func forgetParents(tx *gorm.DB, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id.String()
	}
	// An array literal (UUIDs need no quoting), as gorm expands a slice into a list;
	// jsonb_exists_any, as gorm takes the ?| operator for a placeholder.
	array := "{" + strings.Join(names, ",") + "}"
	return tx.Exec("UPDATE synthetic_checks SET parent_ids = parent_ids - CAST(? AS text[]), "+
		"version = version + 1 WHERE jsonb_exists_any(parent_ids, CAST(? AS text[]))", array, array).Error
}

// applyImport creates, updates and deletes in one transaction; an update whose monitor's version
// moved on since the plan fails it all with ErrConflict.
func (r *repository) applyImport(create, update []*Check, deleteIDs []uuid.UUID, windows maintenancePlan) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := applyMaintenancePlan(tx, windows); err != nil {
			return err
		}
		for _, check := range create {
			if err := tx.Create(check).Error; err != nil {
				return err
			}
		}
		for _, check := range update {
			result := tx.Model(&Check{}).Where("id = ? AND version = ?", check.ID, check.Version-1).
				Select("*").Omit("id", "created_at", "created_by").Updates(check)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
		}
		if len(deleteIDs) > 0 {
			if err := forgetParents(tx, deleteIDs); err != nil {
				return err
			}
			return tx.Where("id IN ?", deleteIDs).Delete(&Check{}).Error
		}
		return nil
	})
}

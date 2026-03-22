package storage

import (
	"context"
	"database/sql"
	"fmt"

	"sprawler/internal/batchwriter"
	sqlcdb "sprawler/internal/database/sqlc"
	"sprawler/internal/model"
)

// Typed kind definitions -- bind each entity to its model type at compile time.
var (
	KindSites        = batchwriter.NewKind[model.Site]("sites")
	KindSiteUsers    = batchwriter.NewKind[model.SiteUser]("site_users")
	KindUserProfiles = batchwriter.NewKind[model.UserProfile]("user_profiles")
	KindSiteGroups   = batchwriter.NewKind[model.SiteGroup]("site_groups")
	KindGroupMembers = batchwriter.NewKind[model.GroupMember]("group_members")
)

func registerSQLCOperations(queue *batchwriter.BatchWriter, db *sql.DB, queries *sqlcdb.Queries, useInserts bool) {
	// active points to tx-scoped queries during a flush, original queries otherwise.
	// Safe without synchronization: single writer goroutine.
	active := queries
	var tx *sql.Tx

	queue.SetFlushHooks(&batchwriter.FlushHooks{
		Before: func(ctx context.Context) (context.Context, error) {
			var err error
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return ctx, fmt.Errorf("begin tx: %w", err)
			}
			active = queries.WithTx(tx)
			return ctx, nil
		},
		After: func(ctx context.Context, err error) error {
			defer func() { active = queries }()
			if err != nil {
				tx.Rollback()
				return err
			}
			return tx.Commit()
		},
	})

	registerSites(queue, &active, useInserts)
	registerSiteUsers(queue, &active, useInserts)
	registerUserProfiles(queue, &active, useInserts)
	registerSiteGroups(queue, &active, useInserts)
	registerGroupMembers(queue, &active, useInserts)
}

// registerEntity registers a typed batch handler that reads tx-scoped queries via the shared pointer.
func registerEntity[M any, P any](
	queue *batchwriter.BatchWriter,
	kind batchwriter.Kind[M],
	active **sqlcdb.Queries,
	useInserts bool,
	toParams func(M) P,
	insertFn func(*sqlcdb.Queries, context.Context, P) error,
	upsertFn func(*sqlcdb.Queries, context.Context, P) error,
	errLabel func(M) string,
) {
	op, verb := upsertFn, "upsert"
	if useInserts {
		op, verb = insertFn, "insert"
	}
	batchwriter.RegisterTyped(queue, kind, func(ctx context.Context, item M) error {
		q := *active
		if err := op(q, ctx, toParams(item)); err != nil {
			return fmt.Errorf("failed to %s %s: %w", verb, errLabel(item), err)
		}
		return nil
	})
}

func nullStringFromPtr(p *string) sql.NullString {
	if p != nil && *p != "" {
		return sql.NullString{String: *p, Valid: true}
	}
	return sql.NullString{}
}

// --- Per-entity converters and registrations ---

func siteParams(s model.Site) sqlcdb.UpsertSiteParams {
	return sqlcdb.UpsertSiteParams{
		SiteID:         s.SiteId,
		SiteUrl:        s.SiteUrl,
		TimeCreated:    sql.NullString{String: s.TimeCreated, Valid: s.TimeCreated != ""},
		Modified:       sql.NullString{String: s.Modified, Valid: s.Modified != ""},
		Title:          sql.NullString{String: s.Title, Valid: s.Title != ""},
		TemplateName:   sql.NullString{String: s.TemplateName, Valid: s.TemplateName != ""},
		CreatedByEmail: sql.NullString{String: s.CreatedByEmail, Valid: s.CreatedByEmail != ""},
		GroupID:        sql.NullString{String: s.GroupId, Valid: s.GroupId != ""},
		LastActivityOn: sql.NullString{String: s.LastActivityOn, Valid: s.LastActivityOn != ""},
		StorageUsed:    sql.NullFloat64{Float64: s.StorageUsed, Valid: s.StorageUsed > 0},
		LockState:      sql.NullString{String: s.LockState, Valid: s.LockState != ""},
	}
}

func registerSites(queue *batchwriter.BatchWriter, active **sqlcdb.Queries, useInserts bool) {
	registerEntity(queue, KindSites, active, useInserts, siteParams,
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteParams) error {
			return txq.InsertSite(ctx, sqlcdb.InsertSiteParams(p))
		},
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteParams) error {
			return txq.UpsertSite(ctx, p)
		},
		func(s model.Site) string {
			return fmt.Sprintf("site %s", s.SiteId)
		},
	)
}

func siteUserParams(u model.SiteUser) sqlcdb.UpsertSiteUserParams {
	return sqlcdb.UpsertSiteUserParams{
		ID:                             sql.NullInt64{Int64: int64(u.ID), Valid: true},
		SiteID:                         sql.NullString{String: u.SiteId, Valid: u.SiteId != ""},
		Email:                          sql.NullString{String: u.Email, Valid: u.Email != ""},
		Expiration:                     sql.NullString{String: u.Expiration, Valid: u.Expiration != ""},
		IsEmailAuthenticationGuestUser: sql.NullBool{Bool: u.IsEmailAuthenticationGuestUser, Valid: true},
		IsHiddenInUi:                   sql.NullBool{Bool: u.IsHiddenInUI, Valid: true},
		IsShareByEmailGuestUser:        sql.NullBool{Bool: u.IsShareByEmailGuestUser, Valid: true},
		IsSiteAdmin:                    sql.NullBool{Bool: u.IsSiteAdmin, Valid: true},
		LoginName:                      sql.NullString{String: u.LoginName, Valid: u.LoginName != ""},
		PrincipalType:                  sql.NullInt64{Int64: int64(u.PrincipalType), Valid: true},
		Title:                          sql.NullString{String: u.Title, Valid: u.Title != ""},
		UseridNameID:                   sql.NullString{String: u.UserId.NameId, Valid: u.UserId.NameId != ""},
		UseridNameIDIssuer:             sql.NullString{String: u.UserId.NameIdIssuer, Valid: u.UserId.NameIdIssuer != ""},
		UserPrincipalName:              nullStringFromPtr(u.UserPrincipalName),
		Updated:                        sql.NullString{String: u.Updated, Valid: u.Updated != ""},
	}
}

func registerSiteUsers(queue *batchwriter.BatchWriter, active **sqlcdb.Queries, useInserts bool) {
	registerEntity(queue, KindSiteUsers, active, useInserts, siteUserParams,
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteUserParams) error {
			return txq.InsertSiteUser(ctx, sqlcdb.InsertSiteUserParams(p))
		},
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteUserParams) error {
			return txq.UpsertSiteUser(ctx, p)
		},
		func(u model.SiteUser) string {
			return fmt.Sprintf("site user %d in site %s", u.ID, u.SiteId)
		},
	)
}

func userProfileParams(p model.UserProfile) sqlcdb.UpsertUserProfileParams {
	return sqlcdb.UpsertUserProfileParams{
		PersonalUrl:                    p.PersonalUrl,
		DocID:                          sql.NullString{String: p.DocId, Valid: p.DocId != ""},
		AadObjectID:                    sql.NullString{String: p.AadObjectId, Valid: p.AadObjectId != ""},
		AccountName:                    sql.NullString{String: p.AccountName, Valid: p.AccountName != ""},
		PersonalSiteInstantiationState: sql.NullString{String: p.PersonalSiteInstantiationState, Valid: p.PersonalSiteInstantiationState != ""},
		ProfileSid:                     sql.NullString{String: p.ProfileSid, Valid: p.ProfileSid != ""},
		UserProfileGuid:                sql.NullString{String: p.UserProfileGuid, Valid: p.UserProfileGuid != ""},
		UserPrincipalName:              sql.NullString{String: p.UserPrincipalName, Valid: p.UserPrincipalName != ""},
		LastModifiedTime:               sql.NullString{String: p.LastModifiedTime, Valid: p.LastModifiedTime != ""},
	}
}

func registerUserProfiles(queue *batchwriter.BatchWriter, active **sqlcdb.Queries, useInserts bool) {
	registerEntity(queue, KindUserProfiles, active, useInserts, userProfileParams,
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertUserProfileParams) error {
			return txq.InsertUserProfile(ctx, sqlcdb.InsertUserProfileParams(p))
		},
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertUserProfileParams) error {
			return txq.UpsertUserProfile(ctx, p)
		},
		func(p model.UserProfile) string {
			return fmt.Sprintf("user profile %s", p.PersonalUrl)
		},
	)
}

func siteGroupParams(g model.SiteGroup) sqlcdb.UpsertSiteGroupsBatchParams {
	return sqlcdb.UpsertSiteGroupsBatchParams{
		ID:          int64(g.ID),
		Title:       g.Title,
		LoginName:   g.LoginName,
		Description: sql.NullString{String: g.Description, Valid: g.Description != ""},
		OwnerTitle:  sql.NullString{String: g.OwnerTitle, Valid: g.OwnerTitle != ""},
		SiteID:      g.SiteId,
		Updated:     g.Updated,
	}
}

func registerSiteGroups(queue *batchwriter.BatchWriter, active **sqlcdb.Queries, useInserts bool) {
	registerEntity(queue, KindSiteGroups, active, useInserts, siteGroupParams,
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteGroupsBatchParams) error {
			return txq.InsertSiteGroupsBatch(ctx, sqlcdb.InsertSiteGroupsBatchParams(p))
		},
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertSiteGroupsBatchParams) error {
			return txq.UpsertSiteGroupsBatch(ctx, p)
		},
		func(g model.SiteGroup) string {
			return fmt.Sprintf("site group %d in site %s", g.ID, g.SiteId)
		},
	)
}

func groupMemberParams(m model.GroupMember) sqlcdb.UpsertGroupMembersBatchParams {
	return sqlcdb.UpsertGroupMembersBatchParams{
		ID:                int64(m.ID),
		Title:             m.Title,
		LoginName:         m.LoginName,
		Email:             sql.NullString{String: m.Email, Valid: m.Email != ""},
		PrincipalType:     int64(m.PrincipalType),
		UserPrincipalName: nullStringFromPtr(m.UserPrincipalName),
		GroupID:           int64(m.GroupId),
		SiteID:            m.SiteId,
		Updated:           m.Updated,
	}
}

func registerGroupMembers(queue *batchwriter.BatchWriter, active **sqlcdb.Queries, useInserts bool) {
	registerEntity(queue, KindGroupMembers, active, useInserts, groupMemberParams,
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertGroupMembersBatchParams) error {
			return txq.InsertGroupMembersBatch(ctx, sqlcdb.InsertGroupMembersBatchParams(p))
		},
		func(txq *sqlcdb.Queries, ctx context.Context, p sqlcdb.UpsertGroupMembersBatchParams) error {
			return txq.UpsertGroupMembersBatch(ctx, p)
		},
		func(m model.GroupMember) string {
			return fmt.Sprintf("group member %d in group %d site %s", m.ID, m.GroupId, m.SiteId)
		},
	)
}

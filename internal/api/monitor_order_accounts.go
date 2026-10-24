package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// monitoredOrderAccounts keeps task accounts separate from fallback accounts
// so callers can preserve task priority while rotating within each pool.
type monitoredOrderAccounts struct {
	task        []platformAccount
	fallback    []platformAccount
	unavailable []string
}

// resolveMonitoredOrderAccounts mirrors the Python order account loader:
// task accounts first, then user/category, user/global, admin/category, and
// admin/global fallbacks. Account IDs are globally unique, but OwnerID is read
// from xy_accounts and retained because admin fallback accounts can belong to
// a different owner than the monitored item.
func (s *Server) resolveMonitoredOrderAccounts(ctx context.Context, ownerID, categoryID int64, taskAccountIDs []string) (monitoredOrderAccounts, error) {
	var result monitoredOrderAccounts
	category := any(nil)
	if categoryID > 0 {
		category = categoryID
	}

	orderedFallbackIDs := make([]string, 0)
	seenFallbackIDs := make(map[string]bool)
	addFallbacks := func(ids []string) {
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && !seenFallbackIDs[id] {
				seenFallbackIDs[id] = true
				orderedFallbackIDs = append(orderedFallbackIDs, id)
			}
		}
	}

	userCategoryIDs, err := s.fallbackOrderAccountIDs(ctx, `owner_id=?`, []any{ownerID}, category)
	if err != nil {
		return result, err
	}
	addFallbacks(userCategoryIDs)
	userGlobalIDs, err := s.fallbackOrderAccountIDs(ctx, `owner_id=?`, []any{ownerID}, nil)
	if err != nil {
		return result, err
	}
	addFallbacks(userGlobalIDs)
	adminCategoryIDs, err := s.fallbackOrderAccountIDs(ctx, `owner_id IN (SELECT id FROM xy_users WHERE role='ADMIN')`, nil, category)
	if err != nil {
		return result, err
	}
	addFallbacks(adminCategoryIDs)
	adminGlobalIDs, err := s.fallbackOrderAccountIDs(ctx, `owner_id IN (SELECT id FROM xy_users WHERE role='ADMIN')`, nil, nil)
	if err != nil {
		return result, err
	}
	addFallbacks(adminGlobalIDs)

	taskIDs := uniqueMonitorAccountIDs(taskAccountIDs)
	seenIDs := make(map[string]bool, len(taskIDs)+len(orderedFallbackIDs))
	for _, id := range taskIDs {
		seenIDs[id] = true
		account, reason, found, loadErr := s.loadMonitoredOrderAccount(ctx, id)
		if loadErr != nil {
			return result, loadErr
		}
		if !found || reason != "" {
			result.unavailable = append(result.unavailable, reason)
			continue
		}
		result.task = append(result.task, account)
	}
	for _, id := range orderedFallbackIDs {
		if seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		account, reason, found, loadErr := s.loadMonitoredOrderAccount(ctx, id)
		if loadErr != nil {
			return result, loadErr
		}
		if !found || reason != "" {
			result.unavailable = append(result.unavailable, reason)
			continue
		}
		result.fallback = append(result.fallback, account)
	}
	return result, nil
}

func (s *Server) fallbackOrderAccountIDs(ctx context.Context, ownerClause string, ownerArgs []any, categoryID any) ([]string, error) {
	query := `SELECT data FROM resource_records WHERE kind='order_fallback_account' AND enabled=1 AND ` + ownerClause + ` AND json_extract(data,'$.category_id') IS ? ORDER BY owner_id,id`
	if ownerClause == `owner_id=?` {
		// The Python service chooses one current user's configuration per scope.
		query += ` LIMIT 1`
	}
	args := append(append([]any{}, ownerArgs...), categoryID)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		data := map[string]any{}
		if err = json.Unmarshal([]byte(raw), &data); err != nil {
			continue
		}
		ids = append(ids, monitorAccountIDs(data["account_ids"])...)
	}
	return ids, rows.Err()
}

func uniqueMonitorAccountIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

func (s *Server) loadMonitoredOrderAccount(ctx context.Context, accountID string) (platformAccount, string, bool, error) {
	var account platformAccount
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,account_id,cookie,COALESCE(proxy_type,'none'),COALESCE(proxy_host,''),proxy_port,proxy_user,proxy_pass,status FROM xy_accounts WHERE account_id=?`, accountID).Scan(&account.ID, &account.OwnerID, &account.AccountID, &account.Cookie, &account.ProxyType, &account.ProxyHost, &account.ProxyPort, &account.ProxyUser, &account.ProxyPass, &status)
	if err == sql.ErrNoRows {
		return platformAccount{}, fmt.Sprintf("账号%s不存在", accountID), false, nil
	}
	if err != nil {
		return platformAccount{}, "", false, err
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "active"
	}
	switch status {
	case "inactive", "disabled", "suspended", "deleted":
		return account, fmt.Sprintf("账号%s已停用(状态=%s)", accountID, status), true, nil
	}
	if strings.TrimSpace(account.Cookie) == "" {
		return account, fmt.Sprintf("账号%s Cookie缺失", accountID), true, nil
	}
	return account, "", true, nil
}

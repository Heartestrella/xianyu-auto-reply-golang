package api

import "context"

// Delete live configuration atomically; orders and event logs remain as history.
func (s *Server) deleteAccountData(ctx context.Context, ownerID int64, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Relations have no account column. Remove only this owner's own-item links,
	// and retain links still used by another account belonging to that owner.
	_, err = tx.ExecContext(ctx, `DELETE FROM card_item_relations
		WHERE source='own' AND card_id IN (SELECT id FROM cards WHERE user_id=?)
		AND item_id IN (SELECT item_id FROM xy_catalog_items WHERE owner_id=? AND account_id=?)
		AND item_id NOT IN (SELECT item_id FROM xy_catalog_items WHERE owner_id=? AND account_id<>?)`, ownerID, ownerID, accountID, ownerID, accountID)
	if err != nil {
		return err
	}
	for _, table := range []string{"xy_catalog_items", "xy_keyword_rules", "default_replies", "default_reply_records", "resource_records"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE owner_id=? AND account_id=?`, ownerID, accountID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM xy_auto_relist_rules WHERE user_id=? AND account_id=?`, ownerID, accountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM cookie_refresh_schedules WHERE account_id=?`, accountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM xy_accounts WHERE owner_id=? AND account_id=?`, ownerID, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

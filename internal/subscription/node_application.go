package subscription

import (
	"context"

	"ctlvps/internal/domain"
)

// Current authority always wins over an old successful receipt. No subscription
// response cache may resurrect a revoked/paused/expired/exhausted share node.
func (s *Service) subscriptionNode(ctx context.Context, n domain.Node) (domain.Node, bool, error) {
	if n.Source == domain.NodeChain {
		landing, found, err := s.Store.ChainLanding(ctx, n.ID)
		if err != nil || !found {
			return n, false, err
		}
		projected, usable, err := s.subscriptionNode(ctx, landing)
		if err != nil || !usable {
			return n, false, err
		}
		n.Protocol, n.Server, n.Port, n.Params = projected.Protocol, projected.Server, projected.Port, projected.Params
		return n, n.Enabled && !n.Revoked, nil
	}
	if n.ShareID != nil {
		share, err := s.Store.GetShare(ctx, *n.ShareID)
		if err != nil {
			return n, false, err
		}
		if share.NodeExhausted(n) || share.Status != domain.ShareActive || (share.ExpiresAt != nil && !s.Now().Before(*share.ExpiresAt)) || (share.QuotaBytes > 0 && domain.QuotaBilled(share.BillingMode, share.UsedUpload, share.UsedDownload) >= share.QuotaBytes) {
			return n, false, nil
		}
	}
	return s.Store.AppliedSubscriptionNode(ctx, n)
}

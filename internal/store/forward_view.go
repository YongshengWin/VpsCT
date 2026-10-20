package store

import (
	"context"

	"ctlvps/internal/domain"
)

type PortForwardView struct {
	RouteID string `json:"route_id,omitempty"`
	domain.PortForward
	Rx30Days      int64 `json:"rx_30_days"`
	Tx30Days      int64 `json:"tx_30_days"`
	ReservedPorts []int `json:"reserved_ports"`
}

func (s *Store) PortForwardViews(ctx context.Context, serverID int64) ([]PortForwardView, error) {
	forwards, err := s.ListPortForwards(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out, index := make([]PortForwardView, len(forwards)), map[int64]int{}
	for i, f := range forwards {
		out[i] = PortForwardView{PortForward: f, ReservedPorts: []int{}}
		index[f.ID] = i
	}
	owned, err := s.db.QueryContext(ctx, `SELECT id,entry_forward_id,COALESCE(ix_forward_id,0) FROM access_routes WHERE entry_server_id=? OR ix_server_id=?`, serverID, serverID)
	if err != nil {
		return nil, err
	}
	for owned.Next() {
		var route string
		var entry, ix int64
		if err = owned.Scan(&route, &entry, &ix); err != nil {
			owned.Close()
			return nil, err
		}
		if i, ok := index[entry]; ok {
			out[i].RouteID = route
		}
		if i, ok := index[ix]; ok {
			out[i].RouteID = route
		}
	}
	err = owned.Err()
	owned.Close()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.subject_id,SUM(t.up),SUM(t.down) FROM traffic_daily t JOIN port_forwards f ON f.id=t.subject_id WHERE t.subject='forward' AND f.server_id=? AND t.bucket>=? GROUP BY t.subject_id`, serverID, s.Now().UTC().AddDate(0, 0, -29).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, rx, tx int64
		if err = rows.Scan(&id, &rx, &tx); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Rx30Days, out[i].Tx30Days = rx, tx
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT resource_id,listen_port FROM server_listener_reservations WHERE server_id=? AND resource_kind='forward' ORDER BY listen_port`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var port int
		if err = rows.Scan(&id, &port); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].ReservedPorts = append(out[i].ReservedPorts, port)
		}
	}
	return out, rows.Err()
}

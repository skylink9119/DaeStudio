package api

import (
	"net/http"
	"strings"
)

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "请指定要删除的快照"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, snapshot := range s.state.Snapshots {
		if snapshot.ID == req.ID {
			index = i
			break
		}
	}
	if index < 0 {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "快照不存在或已删除"})
		return
	}
	previous := s.state.Clone()
	s.state.Snapshots = append(s.state.Snapshots[:index:index], s.state.Snapshots[index+1:]...)
	if err := s.persistStateUnlocked(); err != nil {
		s.state = previous
		writeJSON(w, 500, map[string]any{"ok": false, "error": "删除快照保存失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "snapshots": clientState(s.state).Snapshots, "revision": s.state.Revision})
}

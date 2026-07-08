package jobs

// ManagerPolicies tunes stale-job killing and optional semantic dedup (stored on
// Manager; duplicate matching logic lives in internal/agent).
type ManagerPolicies struct {
	IdleKillSecDefault int
	IdleKillByKind     map[string]int // keys: "bash", "task", "skill"
	SemanticDedup      SemanticDedupPolicy
}

// SemanticDedupPolicy is read by agent.findRunningDuplicateTask via Manager.
type SemanticDedupPolicy struct {
	Enabled          bool
	Threshold        float64 // Jaccard similarity in [0,1]
	RequireSameLabel bool
}

// DefaultManagerPolicies returns built-in defaults when config omits values.
func DefaultManagerPolicies() ManagerPolicies {
	return ManagerPolicies{
		IdleKillSecDefault: 120,
		IdleKillByKind: map[string]int{
			"bash": 1800,
			"task": 600,
			"skill": 600,
		},
		SemanticDedup: SemanticDedupPolicy{
			Enabled:          true,
			Threshold:        0.85,
			RequireSameLabel: false,
		},
	}
}

// Configure applies policies (safe to call once at session start).
func (m *Manager) Configure(p ManagerPolicies) {
	if m == nil {
		return
	}
	if p.IdleKillSecDefault <= 0 {
		p.IdleKillSecDefault = DefaultManagerPolicies().IdleKillSecDefault
	}
	if p.IdleKillByKind == nil {
		p.IdleKillByKind = map[string]int{}
	}
	def := DefaultManagerPolicies()
	for k, v := range def.IdleKillByKind {
		if p.IdleKillByKind[k] <= 0 {
			p.IdleKillByKind[k] = v
		}
	}
	if p.SemanticDedup.Threshold <= 0 || p.SemanticDedup.Threshold > 1 {
		if p.SemanticDedup.Enabled {
			p.SemanticDedup.Threshold = def.SemanticDedup.Threshold
		}
	}
	m.mu.Lock()
	m.idleKillDefault = p.IdleKillSecDefault
	m.idleKillByKind = p.IdleKillByKind
	m.semanticDedup = p.SemanticDedup
	m.mu.Unlock()
}

func (m *Manager) SemanticDedupPolicy() SemanticDedupPolicy {
	if m == nil {
		return DefaultManagerPolicies().SemanticDedup
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.semanticDedup
}

func (m *Manager) idleKillSeconds(kind string) int {
	if m == nil {
		return DefaultManagerPolicies().IdleKillSecDefault
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sec, ok := m.idleKillByKind[kind]; ok && sec > 0 {
		return sec
	}
	if m.idleKillDefault > 0 {
		return m.idleKillDefault
	}
	return 120
}
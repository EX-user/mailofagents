package store

import (
	"encoding/json"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Management-overview aggregation (v0.6, contract finalized 2026-08-24):
// one scan of the message store derives BOTH the per-subordinate summary
// table and the connection-graph shapes. All aggregates derive from
// "subordinate read-only visible" data — this adds no new visibility
// surface. Counterparty matching lowercases addresses (accounts are
// lowercase-canonical since v0.5.16, but messages may carry legacy mixed
// case).

const mgmtWindowDays = 7

// MgmtTopContact is one high-frequency counterparty of a core node.
type MgmtTopContact struct {
	Address string `json:"address"`
	Count   int    `json:"count"`
}

// MgmtSubSummary is one row of the overview table.
type MgmtSubSummary struct {
	Address     string           `json:"address"`
	Signature   string           `json:"signature"`
	LastInAt    int64            `json:"last_in_at"`  // unix s, 0 = never (ALL TIME)
	LastOutAt   int64            `json:"last_out_at"` // unix s, 0 = never (ALL TIME)
	CountIn7d   int              `json:"count_in_7d"`
	CountOut7d  int              `json:"count_out_7d"`
	AvgLenIn    int              `json:"avg_len_in"`   // mean body runes, 7d window; 0 = no mail
	AvgLenOut   int              `json:"avg_len_out"`  // mean body runes, 7d window; 0 = no mail
	TopContacts []MgmtTopContact `json:"top_contacts"` // ≤3, 7d window, in+out combined
	LastReadAt  int64            `json:"last_read_at"` // latest unread->read transition, unix s, 0 = never (ALL TIME; liveness weak evidence)
}

// MgmtNode is one graph node. Kind: self | sub | external.
type MgmtNode struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Volume  int    `json:"volume"` // 7d window in+out
}

// MgmtEdge is one graph edge between two nodes. Directional counts are
// explicit (a_to_b / b_to_a — the contract's no-ambiguity clause); they
// cover the 7d window, while last_at is the pair's most recent message
// over ALL TIME (a liveness signal, like last_in/out).
type MgmtEdge struct {
	A      string `json:"a"`
	B      string `json:"b"`
	AToB   int    `json:"a_to_b"`
	BToA   int    `json:"b_to_a"`
	LastAt int64  `json:"last_at"`
}

// MgmtGraph is the graph-view payload (connection view consumes it; the
// view itself is deferred but the shape ships with the same scan).
type MgmtGraph struct {
	Nodes []MgmtNode `json:"nodes"`
	Edges []MgmtEdge `json:"edges"`
}

// MgmtOverview is the merged subs-overview response.
type MgmtOverview struct {
	WindowDays int              `json:"window_days"`
	Subs       []MgmtSubSummary `json:"subs"`
	Graph      MgmtGraph        `json:"graph"`
}

// pairKey orders two addresses so an unordered pair has one map key.
func pairKey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// MgmtSubsOverview aggregates the given account's subordinate overview +
// connection graph in one pass over bMessages. An account with no declared
// subordinates gets empty subs AND an empty graph (no self node) — the
// frontend's empty-state card keys off subs.length == 0. Graph edges exist
// only when the pair exchanged mail within the 7d window; last_at on an
// edge is all-time.
func (s *Store) MgmtSubsOverview(me string) (*MgmtOverview, error) {
	return s.MgmtSubsOverviewWindow(me, mgmtWindowDays)
}

// MgmtSubsOverviewWindow is the range-parameterized form (superior request:
// graph range button 7d/30d/all). days 0 = all time; positive = day window.
func (s *Store) MgmtSubsOverviewWindow(me string, days int) (*MgmtOverview, error) {
	me = strings.ToLower(me)
	edgesList := s.SubordinatesOf(me) // cursor order (address asc), signatures included

	out := &MgmtOverview{WindowDays: days}
	if len(edgesList) == 0 {
		out.Subs = []MgmtSubSummary{}
		out.Graph.Nodes = []MgmtNode{}
		out.Graph.Edges = []MgmtEdge{}
		return out, nil
	}

	subs := make([]MgmtSubSummary, 0, len(edgesList))
	subSet := map[string]string{} // lower addr -> signature
	for _, e := range edgesList {
		addr := strings.ToLower(e.Address)
		subSet[addr] = e.Signature
		subs = append(subs, MgmtSubSummary{Address: addr, Signature: e.Signature})
	}

	// 0 = all time: every message counts as in-window.
	var windowStart int64
	if days > 0 {
		windowStart = s.now().Unix() - int64(days)*86400
	}

	type acc struct {
		lastIn, lastOut   int64
		countIn, countOut int
		sumIn, sumOut     int // body runes, 7d window
	}
	core := map[string]*acc{me: {}}
	for a := range subSet {
		core[a] = &acc{}
	}
	// contacts[c][x] = 7d combined message count between core c and x.
	contacts := map[string]map[string]int{}
	// dir7[from][to] = 7d directional count (lowercased).
	dir7 := map[string]map[string]int{}
	// pairLastAll[pair] = all-time latest ReceivedAt for the pair.
	pairLastAll := map[[2]string]int64{}

	addDir := func(from, to string, at int64, inWindow bool) {
		if inWindow {
			if dir7[from] == nil {
				dir7[from] = map[string]int{}
			}
			dir7[from][to]++
		}
		k := pairKey(from, to)
		if at > pairLastAll[k] {
			pairLastAll[k] = at
		}
	}

	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bMessages).ForEach(func(_, v []byte) error {
			var m Message
			if json.Unmarshal(v, &m) != nil {
				return nil // skip corrupt records, keep counting
			}
			from := strings.ToLower(m.From)
			recips := map[string]bool{}
			for _, a := range m.To {
				recips[strings.ToLower(a)] = true
			}
			for _, a := range m.CC {
				recips[strings.ToLower(a)] = true
			}
			// Contact/edge attribution (contract change 01M0T4RY): a
			// message's counterparty pair is (from, to[0]) ONLY — cc and
			// 2nd/3rd recipients never feed top_contacts or the graph.
			// Mailbox counts (count_in/last_in …) still honor the full
			// recipient set ("其余不变").
			attribTo := ""
			if len(m.To) > 0 {
				attribTo = strings.ToLower(m.To[0])
			}
			bodyRunes := len([]rune(m.Body))
			inWindow := m.ReceivedAt >= windowStart

			// Out activity of a core sender.
			if c, ok := core[from]; ok {
				if m.ReceivedAt > c.lastOut {
					c.lastOut = m.ReceivedAt
				}
				if inWindow {
					c.countOut++
					c.sumOut += bodyRunes
				}
				if attribTo != "" {
					addDir(from, attribTo, m.ReceivedAt, inWindow)
					if inWindow {
						if contacts[from] == nil {
							contacts[from] = map[string]int{}
						}
						contacts[from][attribTo]++
					}
				}
			}
			// In activity of core recipients.
			for r := range recips {
				if c, ok := core[r]; ok {
					if m.ReceivedAt > c.lastIn {
						c.lastIn = m.ReceivedAt
					}
					if inWindow {
						c.countIn++
						c.sumIn += bodyRunes
					}
					_, senderIsCore := core[from]
					if attribTo == r && !senderIsCore {
						// Cross attribution is counted from the sender side
						// when the sender is core; count here only when the
						// sender is external (no double counting).
						addDir(from, r, m.ReceivedAt, inWindow)
						if inWindow {
							if contacts[r] == nil {
								contacts[r] = map[string]int{}
							}
							contacts[r][from]++
						}
					}
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	// Fill the table rows.
	top := func(c string) []MgmtTopContact {
		var list []MgmtTopContact
		for a, n := range contacts[c] {
			list = append(list, MgmtTopContact{Address: a, Count: n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Count != list[j].Count {
				return list[i].Count > list[j].Count
			}
			return list[i].Address < list[j].Address
		})
		if len(list) > 3 {
			list = list[:3]
		}
		if list == nil {
			list = []MgmtTopContact{}
		}
		return list
	}
	for i := range subs {
		a := core[subs[i].Address]
		subs[i].LastInAt = a.lastIn
		subs[i].LastOutAt = a.lastOut
		subs[i].CountIn7d = a.countIn
		subs[i].CountOut7d = a.countOut
		if a.countIn > 0 {
			subs[i].AvgLenIn = a.sumIn / a.countIn
		}
		if a.countOut > 0 {
			subs[i].AvgLenOut = a.sumOut / a.countOut
		}
		subs[i].TopContacts = top(subs[i].Address)
		// Liveness weak evidence: the sub's own latest inbox read. Looked
		// up per sub (≤10) outside the message scan; misses stay 0.
		if acc, err := s.GetAccount(subs[i].Address); err == nil {
			subs[i].LastReadAt = s.LastReadAt(acc.UUID)
		}
	}
	out.Subs = subs

	// Graph: external nodes = per-core top-3 non-core counterparties.
	extVolume := map[string]int{}
	extIsTop := map[string]bool{}
	for c := range core {
		for _, t := range top(c) {
			if _, isCore := core[t.Address]; isCore {
				continue
			}
			if !extIsTop[t.Address] {
				extIsTop[t.Address] = true
			}
			extVolume[t.Address] += t.Count
		}
	}
	nodes := []MgmtNode{{Address: me, Kind: "self", Volume: core[me].countIn + core[me].countOut}}
	// Sub nodes must be emitted in a deterministic order (sorted, like the
	// externals below): subSet is a map and Go randomizes map iteration —
	// an unsorted append made node order flaky across runs (bit releases
	// v0.6.3 and v0.6.5 with transient, order-dependent test failures).
	subList := make([]string, 0, len(subSet))
	for a := range subSet {
		subList = append(subList, a)
	}
	sort.Strings(subList)
	for _, a := range subList {
		nodes = append(nodes, MgmtNode{Address: a, Kind: "sub", Volume: core[a].countIn + core[a].countOut})
	}
	extList := make([]string, 0, len(extIsTop))
	for a := range extIsTop {
		extList = append(extList, a)
	}
	sort.Strings(extList)
	for _, a := range extList {
		nodes = append(nodes, MgmtNode{Address: a, Kind: "external", Volume: extVolume[a]})
	}
	out.Graph.Nodes = nodes

	// Edges: only pairs that exchanged mail in the 7d window — and only
	// between rendered nodes. An edge whose endpoint is not in the node
	// set cannot be drawn by the client anyway, and without the filter it
	// lingers in the payload for the whole 7d window after a subordinate
	// removal: the system notification mail itself is traffic to the
	// removed address (caught live on v0.6.5).
	nodeSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeSet[n.Address] = true
	}
	edgeSet := map[[2]string]bool{}
	for from, tos := range dir7 {
		for to := range tos {
			edgeSet[pairKey(from, to)] = true
		}
	}
	edgeKeys := make([][2]string, 0, len(edgeSet))
	for k := range edgeSet {
		edgeKeys = append(edgeKeys, k)
	}
	sort.Slice(edgeKeys, func(i, j int) bool {
		if edgeKeys[i][0] != edgeKeys[j][0] {
			return edgeKeys[i][0] < edgeKeys[j][0]
		}
		return edgeKeys[i][1] < edgeKeys[j][1]
	})
	out.Graph.Edges = []MgmtEdge{}
	for _, k := range edgeKeys {
		if !nodeSet[k[0]] || !nodeSet[k[1]] {
			continue // dangling edge: endpoint not a rendered node
		}
		out.Graph.Edges = append(out.Graph.Edges, MgmtEdge{
			A: k[0], B: k[1],
			AToB:   dir7[k[0]][k[1]],
			BToA:   dir7[k[1]][k[0]],
			LastAt: pairLastAll[k],
		})
	}
	return out, nil
}

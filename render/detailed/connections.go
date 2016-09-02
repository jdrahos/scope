package detailed

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/weaveworks/scope/probe/endpoint"
	"github.com/weaveworks/scope/render"
	"github.com/weaveworks/scope/report"
)

const (
	portKey    = "port"
	portLabel  = "Port"
	countKey   = "count"
	countLabel = "Count"
	number     = "number"
)

// Exported for testing
var (
	NormalColumns = []Column{
		{ID: portKey, Label: portLabel},
		{ID: countKey, Label: countLabel, DefaultSort: true},
	}
	InternetColumns = []Column{
		{ID: "foo", Label: "Remote"},
		{ID: portKey, Label: portLabel},
		{ID: countKey, Label: countLabel, DefaultSort: true},
	}
)

// ConnectionsSummary is the table of connection to/form a node
type ConnectionsSummary struct {
	ID          string       `json:"id"`
	TopologyID  string       `json:"topologyId"`
	Label       string       `json:"label"`
	Columns     []Column     `json:"columns"`
	Connections []Connection `json:"connections"`
}

// Connection is a row in the connections table.
type Connection struct {
	ID       string               `json:"id"`     // ID of this element in the UI.  Must be unique for a given ConnectionsSummary.
	NodeID   string               `json:"nodeId"` // ID of a node in the topology. Optional, must be set if linkable is true.
	Label    string               `json:"label"`
	Linkable bool                 `json:"linkable"`
	Metadata []report.MetadataRow `json:"metadata,omitempty"`
}

type connectionsByID []Connection

func (s connectionsByID) Len() int           { return len(s) }
func (s connectionsByID) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s connectionsByID) Less(i, j int) bool { return s[i].ID < s[j].ID }

// Intermediate type used as a key to dedupe rows
type connection struct {
	remoteNodeID, localNodeID string
	remoteAddr, localAddr     string
	port                      string // always the server-side port
}

func newConnection(n report.Node, node report.Node, port string, endpointID string, localAddr string) connection {
	c := connection{
		localNodeID:  n.ID,
		remoteNodeID: node.ID,
		port:         port,
	}
	// For internet nodes we break out individual addresses, both when
	// the internet node is remote (an incoming connection from the
	// internet) and 'local' (ie you are loading details on the
	// internet node)
	if isInternetNode(n) {
		// We use the *endpoint* ID here since that has the reverse
		// DNS information associated with it.
		c.localNodeID = endpointID
		c.localAddr = localAddr
	}
	return c
}

func (row connection) ID() string {
	return fmt.Sprintf("%s:%s-%s:%s-%s", row.remoteNodeID, row.remoteAddr, row.localNodeID, row.localAddr, row.port)
}

func incomingConnectionsSummary(topologyID string, r report.Report, n report.Node, ns report.Nodes) ConnectionsSummary {
	localEndpointIDs := endpointChildIDsOf(n)

	// For each node which has an edge TO me
	counts := map[connection]int{}
	for _, node := range ns {
		if !node.Adjacency.Contains(n.ID) {
			continue
		}
		// Work out what port they are talking to, and count the number of
		// connections to that port.
		for _, child := range endpointChildrenOf(node) {
			for _, localEndpointID := range child.Adjacency.Intersection(localEndpointIDs) {
				_, localAddr, port, ok := report.ParseEndpointNodeID(localEndpointID)
				if !ok {
					continue
				}
				key := newConnection(n, node, port, localEndpointID, localAddr)
				counts[key] = counts[key] + 1
			}
		}
	}

	columnHeaders := NormalColumns
	if isInternetNode(n) {
		columnHeaders = InternetColumns
	}
	return ConnectionsSummary{
		ID:          "incoming-connections",
		TopologyID:  topologyID,
		Label:       "Inbound",
		Columns:     columnHeaders,
		Connections: connectionRows(r, ns, counts, isInternetNode(n)),
	}
}

func outgoingConnectionsSummary(topologyID string, r report.Report, n report.Node, ns report.Nodes) ConnectionsSummary {
	localEndpoints := endpointChildrenOf(n)

	// For each node which has an edge FROM me
	counts := map[connection]int{}
	for _, id := range n.Adjacency {
		node, ok := ns[id]
		if !ok {
			continue
		}

		remoteEndpointIDs := endpointChildIDsOf(node)

		for _, localEndpoint := range localEndpoints {
			_, localAddr, _, ok := report.ParseEndpointNodeID(localEndpoint.ID)
			if !ok {
				continue
			}

			for _, remoteEndpointID := range localEndpoint.Adjacency.Intersection(remoteEndpointIDs) {
				_, _, port, ok := report.ParseEndpointNodeID(remoteEndpointID)
				if !ok {
					continue
				}
				key := newConnection(n, node, port, localEndpoint.ID, localAddr)
				counts[key] = counts[key] + 1
			}
		}
	}

	columnHeaders := NormalColumns
	if isInternetNode(n) {
		columnHeaders = InternetColumns
	}
	return ConnectionsSummary{
		ID:          "outgoing-connections",
		TopologyID:  topologyID,
		Label:       "Outbound",
		Columns:     columnHeaders,
		Connections: connectionRows(r, ns, counts, isInternetNode(n)),
	}
}

func endpointChildrenOf(n report.Node) []report.Node {
	result := []report.Node{}
	n.Children.ForEach(func(child report.Node) {
		if child.Topology == report.Endpoint {
			result = append(result, child)
		}
	})
	return result
}

func endpointChildIDsOf(n report.Node) report.IDList {
	result := report.MakeIDList()
	n.Children.ForEach(func(child report.Node) {
		if child.Topology == report.Endpoint {
			result = result.Add(child.ID)
		}
	})
	return result
}

func isInternetNode(n report.Node) bool {
	return n.ID == render.IncomingInternetID || n.ID == render.OutgoingInternetID
}

func connectionRows(r report.Report, ns report.Nodes, in map[connection]int, includeLocal bool) []Connection {
	output := []Connection{}
	for row, count := range in {
		// Use MakeNodeSummary to render the id and label of this node
		// TODO(paulbellamy): Would be cleaner if we hade just a
		// MakeNodeID(ns[row.remoteNodeID]). As we don't need the whole summary.
		summary, ok := MakeNodeSummary(r, ns[row.remoteNodeID])
		connection := Connection{
			ID:       row.ID(),
			NodeID:   summary.ID,
			Label:    summary.Label,
			Linkable: true,
		}
		if !ok && row.remoteAddr != "" {
			connection.Label = row.remoteAddr
			connection.Linkable = false
		}
		if includeLocal {
			// Does localNode (which, in this case, is an endpoint)
			// have a DNS record in it?
			label := row.localAddr
			if set, ok := r.Endpoint.Nodes[row.localNodeID].Sets.Lookup(endpoint.ReverseDNSNames); ok && len(set) > 0 {
				label = fmt.Sprintf("%s (%s)", set[0], label)
			}
			connection.Metadata = append(connection.Metadata,
				report.MetadataRow{
					ID:       "foo",
					Value:    label,
					Datatype: number,
				})
		}
		connection.Metadata = append(connection.Metadata,
			report.MetadataRow{
				ID:       portKey,
				Value:    row.port,
				Datatype: number,
			},
			report.MetadataRow{
				ID:       countKey,
				Value:    strconv.Itoa(count),
				Datatype: number,
			},
		)
		output = append(output, connection)
	}
	sort.Sort(connectionsByID(output))
	return output
}

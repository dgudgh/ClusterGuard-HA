package store

import (
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func validNodeName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if index > 0 && (char == '-' || char == '_' || char == '.') {
			continue
		}
		return false
	}
	return true
}

func normalizeNode(node model.DatabaseNode) model.DatabaseNode {
	node.NodeName = strings.TrimSpace(node.NodeName)
	node.DisplayName = strings.TrimSpace(node.DisplayName)
	node.Hostname = strings.TrimSpace(node.Hostname)
	node.IPAddress = strings.TrimSpace(node.IPAddress)
	node.HostClass = strings.TrimSpace(node.HostClass)
	if node.DisplayName == "" {
		node.DisplayName = node.NodeName
	}
	aliases := make([]string, 0, len(node.Aliases))
	for _, alias := range node.Aliases {
		aliases = appendAlias(aliases, alias)
	}
	sort.Strings(aliases)
	node.Aliases = aliases
	return node
}

func validateNode(node model.DatabaseNode) error {
	if !validNodeName(node.NodeName) {
		return validationError("node_name must be 1-64 letters, digits, dots, underscores, or hyphens")
	}
	if !node.Kind.Valid() {
		return validationError("node kind must be data, controller, or mixed")
	}
	if node.Hostname == "" && node.IPAddress == "" {
		return validationError("node hostname or IP address is required")
	}
	if node.IPAddress != "" && net.ParseIP(node.IPAddress) == nil {
		return validationError("node IP address is invalid")
	}
	return nil
}

func nodeAddressCollision(left, right model.DatabaseNode) bool {
	return left.Active && right.Active && ((left.Hostname != "" && right.Hostname != "" && strings.EqualFold(left.Hostname, right.Hostname)) || (left.IPAddress != "" && left.IPAddress == right.IPAddress))
}

func putNodeCandidate(nodes map[model.ResourceID]model.DatabaseNode, node model.DatabaseNode, now time.Time) (model.DatabaseNode, error) {
	node = normalizeNode(node)
	if err := validateNode(node); err != nil {
		return model.DatabaseNode{}, err
	}
	if node.ResourceID == "" {
		node.ResourceID = model.NewResourceID()
	} else if !model.ValidResourceID(node.ResourceID) {
		return model.DatabaseNode{}, validationError("node resource ID is invalid")
	}
	existing, existed := nodes[node.ResourceID]
	if existed && node.NodeName != existing.NodeName {
		return model.DatabaseNode{}, conflictError("node_name is immutable")
	}
	for resourceID, candidate := range nodes {
		if resourceID == node.ResourceID {
			continue
		}
		if strings.EqualFold(candidate.NodeName, node.NodeName) {
			return model.DatabaseNode{}, conflictError("node_name already exists")
		}
		if nodeAddressCollision(candidate, node) {
			return model.DatabaseNode{}, conflictError("active node coordinates already belong to %s", candidate.NodeName)
		}
	}
	if existed {
		if existing.Hostname != "" && !strings.EqualFold(existing.Hostname, node.Hostname) {
			node.Aliases = appendAlias(node.Aliases, existing.Hostname)
		}
		if existing.IPAddress != "" && existing.IPAddress != node.IPAddress {
			node.Aliases = appendAlias(node.Aliases, existing.IPAddress)
		}
		for _, alias := range existing.Aliases {
			node.Aliases = appendAlias(node.Aliases, alias)
		}
		sort.Strings(node.Aliases)
		node.CreatedAt = existing.CreatedAt
		node.MetadataRevision = existing.MetadataRevision + 1
	} else {
		node.CreatedAt = now
		node.MetadataRevision = 1
	}
	node.UpdatedAt = now
	nodes[node.ResourceID] = cloneNode(node)
	return node, nil
}

func (repository *Repository) PutNode(node model.DatabaseNode) (model.DatabaseNode, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	now := repository.now().UTC()
	next := repository.snapshot
	next.Nodes = cloneNodeMap(repository.snapshot.Nodes)
	var err error
	node, err = putNodeCandidate(next.Nodes, node, now)
	if err != nil {
		return model.DatabaseNode{}, err
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneNode(node), err
		}
		return model.DatabaseNode{}, err
	}
	repository.snapshot = next
	return cloneNode(node), nil
}

func (repository *Repository) Node(resourceID model.ResourceID) (model.DatabaseNode, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	node, found := repository.snapshot.Nodes[resourceID]
	return cloneNode(node), found
}

func (repository *Repository) Nodes() []model.DatabaseNode {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	nodes := make([]model.DatabaseNode, 0, len(repository.snapshot.Nodes))
	for _, node := range repository.snapshot.Nodes {
		nodes = append(nodes, cloneNode(node))
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].NodeName != nodes[j].NodeName {
			return nodes[i].NodeName < nodes[j].NodeName
		}
		return nodes[i].ResourceID < nodes[j].ResourceID
	})
	return nodes
}

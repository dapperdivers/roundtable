package util

import "fmt"

// DAGNode represents a node in a directed acyclic graph with dependencies.
type DAGNode struct {
	Name      string
	DependsOn []string
}

// ValidateDAG validates that the given nodes form a valid directed acyclic graph.
// It checks that all dependencies exist and that there are no circular dependencies.
// Uses Kahn's algorithm for topological sorting to detect cycles.
func ValidateDAG(nodes []DAGNode) error {
	if len(nodes) == 0 {
		return nil
	}

	// Build set of all node names
	nodeNames := make(map[string]bool)
	for _, node := range nodes {
		nodeNames[node.Name] = true
	}

	// Verify all dependencies exist
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if !nodeNames[dep] {
				return fmt.Errorf("node %q depends on unknown node %q", node.Name, dep)
			}
		}
	}

	// Kahn's algorithm: compute in-degrees
	inDegree := make(map[string]int)
	for _, node := range nodes {
		inDegree[node.Name] = 0
	}
	for _, node := range nodes {
		for range node.DependsOn {
			inDegree[node.Name]++
		}
	}

	// Initialize queue with nodes that have no dependencies
	queue := []string{}
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	// Process nodes in topological order
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		visited++

		// Find nodes that depend on current and decrement their in-degree
		for _, node := range nodes {
			for _, dep := range node.DependsOn {
				if dep == current {
					inDegree[node.Name]--
					if inDegree[node.Name] == 0 {
						queue = append(queue, node.Name)
					}
				}
			}
		}
	}

	// If we didn't visit all nodes, there's a cycle
	if visited != len(nodes) {
		return fmt.Errorf("circular dependency detected in DAG")
	}

	return nil
}

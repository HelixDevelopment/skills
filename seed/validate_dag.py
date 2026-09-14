#!/usr/bin/env python3
"""Validate that CORPUS.yaml declares a well-formed, acyclic dependency graph.

Checks:
  1. Every requires/extends/recommends/composes/related_to/alternative_to
     (+ part_of alias) edge target resolves to a node declared in
     CORPUS.yaml (closed-world corpus; no dangling edges).
  2. The HARD CLOSURE set {requires, composes, extends} is acyclic
     (research/skill_granularity_and_composition.md §4.2/§4.3). recommends/
     related_to/alternative_to are advisory and are EXCLUDED from the cycle
     check by design — related_to/alternative_to are explicitly symmetric
     relations, and this is a relaxation vs the old
     requires+extends+recommends joint check: nothing that passed before
     fails now (§4.3).
  3. `kind` values are a closed set {atomic, composite, umbrella} (matching
     the skills.kind DB CHECK constraint introduced by
     migrations/002_granularity.up.sql); a node omitting `kind` defaults to
     `atomic` (the column DEFAULT).
  4. Granularity invariants (§5.4 invariant #2/#3): an `atomic` node has ZERO
     outgoing `composes` edges; an `umbrella`/`composite` node has AT LEAST
     ONE outgoing `composes` edge. So the 8 existing seed nodes (which
     declare neither `kind` nor `composes`) pass vacuously.

Usage: python3 validate_dag.py [path/to/CORPUS.yaml]
Exit code 0 on success, 1 on failure (unresolved edges, cycle, invalid `kind`
value, or granularity invariant violation found).
"""
import sys
import yaml

# The hard closure set the "everything needed for X" resolver transitively
# walks, and the set the acyclicity invariant is scoped to (research/
# skill_granularity_and_composition.md §4.2/§4.3). recommends/related_to/
# alternative_to are advisory and excluded.
HARD_CLOSURE_RELATIONS = ("requires", "composes", "extends")

# All relation keys a node may declare edges under (for closed-world
# unresolved-target checking; broader than the hard-closure set so an
# advisory recommends/related_to/alternative_to typo is still caught).
#
# W4 fix (Fable code-review remediation, P1.T1): this tuple used to omit
# related_to/alternative_to despite this exact comment claiming they were
# covered -- a doc-vs-code mismatch that let a `related_to: [ghost-skill]`
# (or `alternative_to: [ghost-skill]`) typo pass validate_dag.py with exit 0
# instead of being reported as an unresolved edge target.
ALL_RELATIONS = ("requires", "extends", "recommends", "composes", "related_to", "alternative_to")

# N1 fix (Fable code-review remediation, P1.T1): the closed set of `kind`
# values the DB CHECK constraint (migrations/002_granularity.up.sql: `kind IN
# ('atomic', 'composite', 'umbrella')`) accepts. Before this fix a typo'd
# `kind: bogus` fell through check_granularity_invariants' if/elif
# unnoticed -- neither branch matches a value that is not 'atomic' and not
# in ('umbrella', 'composite'), so no violation was ever reported and
# validate_dag.py exited 0 on a value the real schema would reject outright.
VALID_KINDS = ("atomic", "composite", "umbrella")


def load_corpus(path):
    with open(path, "rb") as f:
        data = yaml.safe_load(f)
    return data["nodes"]


def build_graph(nodes):
    """Build the full edge set (for unresolved-target checking) and the
    hard-closure-only edge set (for cycle detection).

    `part_of` is the child-authored alias of `composes` (§4.1): a node N
    declaring `part_of: [P]` is normalized to the edge P --composes--> N
    (inverted to the canonical umbrella->component direction), exactly like
    the TOML/Go importer aliasing (models.TOMLDependencies.PartOf).
    """
    names = {n["name"] for n in nodes}
    edges = {n["name"]: [] for n in nodes}          # name -> [(target, relation)], ALL relations
    hard_edges = {n["name"]: [] for n in nodes}      # name -> [(target, relation)], hard-closure only
    unresolved = []

    for n in nodes:
        src = n["name"]
        for relation in ALL_RELATIONS:
            for target in n.get(relation) or []:
                if target not in names:
                    unresolved.append((src, relation, target))
                edges[src].append((target, relation))
                if relation in HARD_CLOSURE_RELATIONS:
                    hard_edges[src].append((target, relation))

        for parent in n.get("part_of") or []:
            if parent not in names:
                unresolved.append((src, "part_of", parent))
                continue
            edges.setdefault(parent, []).append((src, "composes"))
            hard_edges.setdefault(parent, []).append((src, "composes"))

    return names, edges, hard_edges, unresolved


def find_cycle(names, hard_edges):
    """DFS cycle check over the hard-closure edge set only."""
    WHITE, GRAY, BLACK = 0, 1, 2
    color = {n: WHITE for n in names}
    path = []

    def visit(node):
        color[node] = GRAY
        path.append(node)
        for target, _relation in hard_edges.get(node, []):
            if target not in color:
                continue  # unresolved edge, reported separately
            if color[target] == GRAY:
                cycle_start = path.index(target)
                return path[cycle_start:] + [target]
            if color[target] == WHITE:
                result = visit(target)
                if result:
                    return result
        path.pop()
        color[node] = BLACK
        return None

    for node in sorted(names):
        if color[node] == WHITE:
            cycle = visit(node)
            if cycle:
                return cycle
    return None


def check_kind_values(nodes):
    """N1 (Fable code-review remediation, P1.T1): reject a `kind` value
    outside VALID_KINDS -- matching the DB CHECK constraint
    (migrations/002_granularity.up.sql). A node omitting `kind` defaults to
    'atomic' (the column DEFAULT), same as check_granularity_invariants."""
    violations = []
    for n in nodes:
        kind = n.get("kind") or "atomic"
        if kind not in VALID_KINDS:
            violations.append((n["name"], kind))
    return violations


def check_granularity_invariants(nodes, edges):
    """§5.4 invariant #2/#3: atomic <=> zero outgoing composes edges;
    umbrella/composite <=> at least one outgoing composes edge.

    `kind` defaults to 'atomic' when a node omits the key, matching the
    skills.kind DB column DEFAULT (migrations/002_granularity.up.sql).
    `composes` count includes edges materialized from a component's
    `part_of` alias (build_graph inverts part_of onto the parent's edge
    list), so either authoring form is recognised.
    """
    violations = []
    for n in nodes:
        name = n["name"]
        kind = n.get("kind") or "atomic"
        composes_count = sum(1 for _target, relation in edges.get(name, []) if relation == "composes")
        if kind == "atomic" and composes_count > 0:
            violations.append((name, kind, composes_count, "atomic must have zero outgoing composes edges"))
        elif kind in ("umbrella", "composite") and composes_count == 0:
            violations.append((name, kind, composes_count, f"{kind} must have >=1 outgoing composes edge"))
    return violations


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "CORPUS.yaml"
    nodes = load_corpus(path)
    names, edges, hard_edges, unresolved = build_graph(nodes)

    edge_count = sum(len(v) for v in edges.values())

    if unresolved:
        print(f"UNRESOLVED EDGES ({len(unresolved)}):")
        for src, relation, target in unresolved:
            print(f"  {src} --{relation}--> {target}  [target not declared in CORPUS.yaml]")
        sys.exit(1)

    cycle = find_cycle(names, hard_edges)
    if cycle:
        print("CYCLE FOUND (hard closure: requires/composes/extends):")
        print("  " + " -> ".join(cycle))
        sys.exit(1)

    kind_violations = check_kind_values(nodes)
    if kind_violations:
        print(f"INVALID KIND VALUES ({len(kind_violations)}):")
        for name, kind in kind_violations:
            print(f"  {name}: kind={kind!r} not in {VALID_KINDS}")
        sys.exit(1)

    violations = check_granularity_invariants(nodes, edges)
    if violations:
        print(f"GRANULARITY INVARIANT VIOLATIONS ({len(violations)}):")
        for name, kind, count, reason in violations:
            print(f"  {name} (kind={kind}, composes_count={count}): {reason}")
        sys.exit(1)

    print(f"DAG OK ({len(names)} nodes, {edge_count} edges)")
    sys.exit(0)


if __name__ == "__main__":
    main()

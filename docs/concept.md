## 1. Product Vision

DiffDuck is a semantic diff compression and exploration system designed for an era of agentic software development.

As coding agents generate increasingly large volumes of code, traditional line-by-line diff review becomes a bottleneck. Humans cannot reasonably inspect every implementation detail of every change.

DiffDuck changes the unit of understanding from **lines of code to semantic code units and behavioral outcomes**.

Instead of asking a developer to read hundreds of lines to discover what changed, DiffDuck presents a compact, hierarchical account of the change, with progressively deeper detail available on demand.

**North Star:** A developer should be able to understand the scope, structure, intent, and consequences of a large code change in seconds rather than minutes or hours.

## 2. Foundational Philosophy

### 2.1 Trust the implementation

DiffDuck assumes that implementation correctness is primarily the responsibility of the coding agent, tests, and other verification systems.

Its primary purpose is not to establish whether code is correct. Its purpose is to communicate what the code does and what changed.

The product must not force the user to examine implementation details to understand a change.

### 2.2 Compress meaning, not merely text

Compression is not achieved by removing lines, collapsing unchanged regions, or generating a prose summary of a patch.

Instead, DiffDuck translates source changes into a higher-level semantic representation.

For example, the implementation:

```go
func calcTopPer(users []User) float64 {
    // Hundreds of lines of implementation
}
```

Might become:

```text
SERVICE User ADD calculateTopPercentileOfUsers([]User) float64
    AKA calcTopPer(users []User) float64
    Calculates the top percentile of a user set.
```

The compressed representation retains:

- The semantic purpose of the unit.
- Its actual identity in the source.
- Its public or relevant structural contract.
- The nature of the change.
- A brief account of its behavior.

The implementation itself is omitted.

### 2.3 Progressive disclosure

The user should never be required to consume more detail than necessary.

Every level of the compressed representation should be independently understandable, while allowing the user to explore increasingly granular detail.

The product should support understanding at multiple levels:

- Entire change set.
- Feature or behavioral outcome.
- Package, module, or service.
- Type, interface, or domain object.
- Method, function, field, or other code unit.
- Original source diff.

The user controls the depth of inspection.

### 2.4 Preserve accountability

Compression must not silently erase meaningful changes.

Every changed source region must remain traceable to the compressed representation, even when its implementation is hidden.

A concise explanation may omit implementation detail, but it must not misrepresent the scope or meaning of a change.

## 3. Core Product Model

DiffDuck represents a Git diff along two complementary dimensions.

### 3.1 WHAT — Behavioral outcomes

An outcome is a human-readable statement describing an externally meaningful result of the change.

Examples:

```text
ADD Role-based access control to user operations
ADD Date-of-birth validation during registration
CHANGE Session expiration behavior
REMOVE Legacy authentication fallback
```

Outcomes are expressed in terms of capabilities, behaviors, and effects rather than implementation.

An outcome may involve multiple code units across multiple files or services.

### 3.2 WHERE — Compressed structural diff

The compressed diff is a hierarchical representation of the code units affected by the change.

A code unit is any meaningful encapsulation boundary, such as:

- Package or module
- Service or component
- Domain object or type
- Interface
- Method or function
- Field, property, or constant

Code units are recursive. A code unit may contain other code units.

The compressed tree contains changed units and the structural ancestors needed to understand their location.

Unchanged code is omitted.

### 3.3 Outcome-to-code relationships

Every outcome is associated with one or more changed code units.

A code unit may contribute to multiple outcomes.

Selecting an outcome filters the structural diff to the units relevant to that outcome, preserving the ancestors necessary to understand their location.

The complete diff remains accessible independently of outcome filtering.

Changes that cannot confidently be associated with an outcome must remain visible and explicitly unassigned.

## 4. The Compressed Diff

The compressed diff is the central artifact of DiffDuck.

It is not a textual patch with annotations. It is a new semantic representation derived from a patch.

### 4.1 Node representation

Each node may contain:

**Structural identity**

The actual name, signature, and location of the source code unit.

**Semantic identity**

A concise human-readable description or normalized name communicating the unit's purpose.

**Change operation**

One of:

- `ADD` — A new code unit was introduced.
- `REMOVE` — An existing code unit was removed.
- `CHANGE` — An existing code unit was modified.

**Behavioral summary**

A brief statement of the relevant behavior or change.

**Children**

Nested changed code units.

**Source reference**

A traceable reference to the corresponding original source diff.

**Outcome associations**

References to the outcomes the node contributes to.

### 4.2 Example

```text
SERVICE UserService CHANGE
    Introduces role authorization and DOB validation.

    METHOD CreateUser(UserInput) User CHANGE
        Enforces authorization and validates DOB.

    METHOD ValidateDOB(Date) Error ADD
        Rejects invalid or future birth dates.

    METHOD AuthorizeRole(Role) Error ADD
        Enforces role-based access permissions.

DOMAIN User CHANGE
    Adds date-of-birth information.

    FIELD dateOfBirth Date ADD
```

This representation communicates what changed without exposing implementation logic.

### 4.3 Semantic naming

Source code names may be abbreviated, ambiguous, or misleading.

DiffDuck may generate clearer semantic names while preserving actual source identities.

For example:

```text
ADD calculateTopPercentileOfUsers([]User) float64
    AKA calcTopPer(users []User) float64
```

Generated names are explanatory aliases, not source transformations.

Actual source identity must always be recoverable.

### 4.4 Compression boundaries

The default representation should preserve meaningful contracts and omit implementation mechanics.

Preserve:

- Meaningful code unit identities.
- Relevant signatures and data shapes.
- Introduced, removed, or modified interfaces.
- Meaningful behavior changes.
- Structural relationships between changed units.

Omit by default:

- Function bodies.
- Control-flow details.
- Intermediate variables.
- Internal algorithms.
- Routine implementation statements.
- Unchanged code.
- Incidental formatting and syntax noise.

Implementation details may be revealed through deeper inspection, but they must not dominate the compressed view.

### 4.5 Direct versus inherited changes

A parent node may appear because it changed directly or because one of its descendants changed.

DiffDuck must distinguish these cases internally.

A service whose only change is the addition of a method should not be represented as though its own interface or behavior was independently modified, except insofar as the new method changes the service's capabilities.

## 5. Compression Architecture

The conceptual processing pipeline is:

```text
Git Diff
    ↓
Structural Analysis
    ↓
Changed Code Unit Tree
    ↓
Leaf-Level Semantic Compression
    ↓
Recursive Parent Compression
    ↓
Outcome Identification and Association
    ↓
Compressed Diff Model
    ↓
Interactive UX
```

### 5.1 Structural analysis

Analyze the before and after revisions of the code.

Identify meaningful code units and match them across revisions.

Determine which units were added, removed, or changed.

Build a minimal structural tree containing affected units and their ancestors.

An AST-based parser, such as Tree-sitter, is a candidate implementation mechanism.

The product specification does not mandate a particular parser or language model.

### 5.2 Leaf compression

Changed leaf units are compressed using their relevant source changes and sufficient surrounding context.

The output should describe the unit's semantic purpose, contract, and behavioral change.

The implementation body should not survive into the default compressed representation.

### 5.3 Recursive parent compression

Compression proceeds bottom-up through the structural tree.

Each parent is summarized using:

1. Its own direct changes, if any.
2. The compressed representations of its changed children.
3. Any additional context required to understand the parent-level effect.

A parent summary must communicate the aggregate meaning of its subtree rather than merely concatenate child summaries.

For example:

```text
METHOD ValidateDOB ADD
    Rejects invalid birth dates.

METHOD CreateUser CHANGE
    Invokes DOB validation before persistence.
```

May produce:

```text
SERVICE UserService CHANGE
    Enforces date-of-birth validation during user creation.
```

This is recursive semantic compression.

### 5.4 Change classification

DiffDuck should distinguish a code unit's existence from changes to its behavior.

For an existing method, a summary should describe what changed, not simply what the method does.

For a newly added method, describing its purpose may be sufficient.

For a removed method, the summary should explain what capability or behavior was eliminated when it can be established.

### 5.5 Structural identity and matching

Code units may be renamed, moved, extracted, or reorganized.

DiffDuck should attempt to recognize continuity between the before and after revisions.

A renamed method should not automatically appear as an unrelated deletion and addition.

When identity cannot be established confidently, the representation must preserve that uncertainty rather than invent continuity.

## 6. User Experience

### 6.1 Primary interface

The primary interface consists of two coordinated panels.

**Left panel: Outcomes**

A concise list of behavioral outcomes introduced, modified, or removed by the change set.

**Right panel: Compressed Diff**

A navigable structural tree displaying the semantic representation of affected code units.

The two panels provide complementary ways to understand the same underlying change.

### 6.2 Initial state

On opening a diff, the user should immediately see:

- A compact account of the overall change.
- The identified outcomes.
- A compressed structural overview of the affected code.
- Enough information to understand the general scope without expanding anything.

The initial tree should favor high-level comprehension over exhaustive detail.

### 6.3 Outcome selection

Selecting an outcome filters the compressed tree to relevant changed units.

Unrelated nodes disappear from the current view.

Required structural ancestors remain visible to preserve context.

The user can return to the complete change set at any time.

### 6.4 Tree navigation

Each node can be expanded or collapsed.

Expansion reveals its compressed children, not automatically its raw implementation.

The user may continue expanding until reaching the finest available semantic unit.

At any point, the original source diff may be inspected.

### 6.5 Visual language

The interface should optimize for rapid visual scanning.

Use consistent visual distinctions for:

- ADD
- REMOVE
- CHANGE
- Structural depth
- Code unit type
- Selected outcome associations

Color communicates change operations, but meaning must also remain identifiable through text or symbols.

Visual density should be low. Repeated labels, unnecessary prose, and decorative UI should be minimized.

## 7. Product Invariants

These are non-negotiable properties of the system.

**I1 — Complete change coverage**

Every meaningful changed source region must be represented by or traceable through the compressed diff.

**I2 — Structural fidelity**

The hierarchy must accurately reflect the structural relationships of the code units it represents.

**I3 — Semantic fidelity**

Summaries must be grounded in the analyzed code. The system must not knowingly invent behavior, contracts, or effects.

**I4 — Recursive comprehensibility**

Every non-leaf node must provide a meaningful high-level account of the changes represented by its subtree.

**I5 — Implementation independence**

A user must be able to understand the intended change without reading implementation code.

**I6 — Progressive disclosure**

Increasing detail must be optional and controlled by the user.

**I7 — Source traceability**

Every compressed code unit must retain a reference to the source changes from which it was derived.

**I8 — Outcome coverage is not required**

Not every code change must correspond to a recognized behavioral outcome. Unassigned changes remain visible.

**I9 — Outcome filtering does not modify the diff**

Filtering changes the presentation, not the underlying compressed representation or its coverage.

**I10 — Honest uncertainty**

When behavior or structural identity cannot be reliably inferred, the system must communicate uncertainty rather than manufacture a confident explanation.

**I11 — Deterministic structural facts**

Code identities, signatures, and change locations should come from source analysis wherever possible, rather than being invented by a language model.

**I12 — Trust is not verification**

A compressed summary is an explanation of apparent behavior, not proof of implementation correctness.

## 8. Important Product Decisions

### Decision 1: The structural diff is the canonical representation

Outcomes provide a useful semantic lens, but the structural diff remains the complete account of changed code.

### Decision 2: The product is diff-scoped

DiffDuck does not attempt to document the entire codebase.

It represents only the changes between revisions and the context required to understand them.

### Decision 3: Implementation is hidden by default

Raw code remains accessible, but it is not the primary review surface.

### Decision 4: Semantic aliases are allowed

The system may introduce descriptive names that improve comprehension, provided actual source identities are preserved.

### Decision 5: Compression is hierarchical

Summaries are produced at multiple structural levels rather than only at the file or pull-request level.

### Decision 6: The model is independent of the UI

The compressed diff should exist as structured data, not merely rendered text.

This enables different views, filtering, serialization, caching, and integrations.

### Decision 7: Language and framework independence is a goal

The conceptual model should support different programming languages, even if initial implementations support only a subset.

### Decision 8: Prefer comprehension over implementation detail

The success criterion is not how accurately the product reproduces source code. It is how efficiently the user can understand the change without losing meaningful information.

## 9. Non-Goals

DiffDuck is not intended to be:

- A replacement for automated tests.
- A static analyzer or correctness verifier.
- A vulnerability scanner.
- A conventional AI code reviewer.
- A full codebase documentation generator.
- A textual diff beautifier.
- An automatic code refactoring tool.

These systems may complement DiffDuck, but they do not define its purpose.

## 10. Success Criteria

The primary measure of success is **time to accurate understanding**.

A successful DiffDuck experience allows a developer to:

1. Identify the meaningful outcomes of a large change.
2. Understand which code units were affected.
3. Understand how the structural changes support those outcomes.
4. Navigate from a high-level explanation to a precise source location.
5. Do all of this without reading implementation code unless they choose to.

Secondary measures include summary accuracy, coverage of changed code, compression ratio, navigation effort, and user confidence in their understanding.

Compression ratio alone is not sufficient. A shorter representation that omits important meaning is worse than a slightly longer representation that communicates the change faithfully.

## 11. Illustrative End-to-End Experience

**Input:** A Git diff containing 24 changed files, 1,800 additions, and 430 deletions.

The changes implement role-based authorization and date-of-birth validation.

**Output:**

```text
OUTCOMES

[ADD] Role-based authorization
      Restricts user operations according to role.

[ADD] Date-of-birth validation
      Rejects invalid dates during registration.
```

Selecting _Date-of-birth validation_ produces:

```text
SERVICE UserService CHANGE
    Validates DOB during user creation.

    METHOD CreateUser(UserInput) User CHANGE
        Rejects invalid DOB before persistence.

    METHOD ValidateDOB(Date) Error ADD
        Validates date range and rejects future dates.

DOMAIN User CHANGE
    Introduces date-of-birth information.

    FIELD dateOfBirth Date ADD

INTERFACE UserRepository CHANGE
    Supports persistence of date-of-birth data.

DATABASE users CHANGE
    Adds date_of_birth column.
```

The developer can understand the feature, identify its structural footprint, and inspect any individual change.

The 2,230 lines of source changes remain available but are no longer required reading for high-level comprehension.

## 12. Open Design Questions

The following decisions are intentionally deferred:

- How should code units be matched across moves, renames, and extraction refactors?
- What is the ideal minimum semantic unit for each programming language?
- How much context should leaf compression receive to accurately infer behavior?
- Should summary depth and verbosity be configurable?
- How should cross-cutting changes, such as configuration and dependency updates, be represented?
- How should the system distinguish an actual behavior change from a pure refactor?
- How should compression uncertainty be surfaced without overwhelming the interface?
- Should outcome discovery precede structural compression, follow it, or operate iteratively?

These are implementation and product-design questions to resolve experimentally. They should not compromise the foundational model.

## 13. North Star Summary

DiffDuck exists to solve a fundamental mismatch between agentic code production and human code comprehension.

Coding agents produce implementations. Humans need to understand changes.

Traditional diffs expose implementation first and meaning second.

**DiffDuck reverses that relationship.**

It converts a Git diff into a compact, hierarchical semantic model that communicates:

**WHAT changed, WHERE it changed, and WHY those changes matter.**

The user can explore any level of detail, from behavioral outcomes down to the original source, without being forced to read the implementation.

**Git diff in. Compressed diff UX out.**

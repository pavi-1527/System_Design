# B1 Teach-back

## Prompt

A mid-level engineer on your team has spent two years writing Java and is moving to Go. Their first Go service has an
`IPaymentService` interface and a `PaymentServiceImpl`, a base struct they embed to "inherit" shared behaviour, a
`utils` package, and errors that are compared by string. Write them an explanation (**500–800 words, your own words,
no copying from the units, one diagram allowed**, Mermaid or ASCII) of **how to shape types, interfaces, errors and
packages in Go so that a new requirement is new code behind an existing seam**, using the two labs (`checkout`:
payer/stock/notifier/clock behind consumer-side interfaces; `dispatch`: sinks, typed errors, retries) as the running
examples. Write it as something you would actually put in the team wiki.

Then answer, in two or three sentences each, whichever two follow-up questions the tutor picks from the "should-hit"
list.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] **Interfaces at the consumer**: says where an interface is declared (the package that uses it), why (structural
      typing, the consumer knows the subset it needs, the producer doesn't import or get imported), and that they should
      be small (1–3 methods), with a concrete example from the labs.
- [ ] **Composition, not inheritance**: embedding promotes methods but the promoted method runs with the embedded value
      as its receiver (no virtual dispatch), so the Java "override" does not work; names the alternative (named field
      plus forwarding, a small interface parameter, or interface embedding for decorators).
- [ ] **Constructors and invariants**: `New` validates and returns an error (wrapping a sentinel), required dependencies
      are explicit, inputs that are maps/slices are copied, state is unexported, and the zero value is either useful or
      misuse fails loudly.
- [ ] **Errors as values**: wrap with `%w` and context; check with `errors.Is`/`errors.As`, never by string; handle each
      error once (log at the edge, not at every layer); map domain errors to transport codes in one place at the edge.
- [ ] **Package boundaries**: dependencies point at the domain core (adapters import the core, `main` wires them); no
      `util`/`common` package; how an import cycle is broken (consumer interface, move the shared type, merge).
- [ ] **Testability**: seams for time, network and randomness; fakes with state over strict mocks; table-driven tests;
      what the lab's parity test and extension test each prove.
- [ ] **Extension without a rewrite**: states that a requirement change should land as new code behind an existing seam,
      with an example (a new sink or decorator written outside the package), and says what is deliberately *not*
      abstracted.

Should-hit (at least 4 of 7):
- [ ] The typed-nil trap: an interface holding a nil pointer is not nil; return a literal `nil` on success.
- [ ] Pointer versus value receivers, and why a type containing a lock must not be copied (`go vet` copylocks).
- [ ] The characterisation (parity) test before refactoring a god object, and one seam per change.
- [ ] A behavioural contract test for plug-in interfaces (Liskov is more than matching signatures).
- [ ] Premature abstraction: the checklist (a second implementation or a test that needs the seam; behaviour versus
      data; a function value instead of an interface) and why waiting is cheap in Go.
- [ ] Functional options versus a config struct, with when each wins.
- [ ] `internal/`, package naming, and checking the dependency direction with `go list`.

Red flags (any one = revise):
- [ ] Recommends an interface per struct (`IFoo`/`FooImpl`) or putting the interface next to the implementation "so
      people can mock it".
- [ ] Says embedding gives you inheritance or method overriding, or recommends a base struct to share behaviour.
- [ ] Compares or classifies errors by their message text, or recommends `err == ErrX` on wrapped errors.
- [ ] Recommends logging an error at every layer as well as returning it.
- [ ] Recommends a `common`/`util` package to break an import cycle.
- [ ] Says generated mocks asserting call order are the default way to test, or that coverage proves correctness.
- [ ] Treats SOLID as a checklist that requires adding interfaces and layers.

## Recording

`sd record teachback B1 --result <pass|revise> --notes "<missed items>"`

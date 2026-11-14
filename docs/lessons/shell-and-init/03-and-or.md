# Lesson 3: `&&`, `||` and `!`

With an evaluator that returns exit statuses, the shell can finally make
decisions. This lesson adds the three smallest decision-making operators:
`&&` runs a second command only if the first succeeded, `||` runs it only
if the first failed, and `!` inverts a status. The change is mostly in
the parser, which makes this the right lesson for studying how a grammar
gives operators their precedence.

---

## Objectives

After this lesson, a student should be able to:

1. Explain short-circuit evaluation and the shell's rule that a status
   of 0 counts as true.
2. Write the shell grammar for lists, and-or lists and pipelines, and
   explain the precedence it gives each operator.
3. Explain why `a || b && c` means something different in the shell than
   the same expression means in C.
4. Extend a recursive-descent parser with a new level of the grammar.
5. Extend a tokenizer to recognize two-character operators.

---

## Background

### Success is true

In Lesson 1, an exit status of 0 meant success. The shell builds its
logic on that: a command that succeeds counts as *true*, and a command
that fails counts as *false*. This is the opposite of C, where 0 is
false, and students mix the two up for weeks. A useful way to remember
it is that the shell asks "did it work?", and the answer "it worked" is
the single value 0.

### Short-circuit evaluation

`a && b` runs `a`. If `a` succeeds, it runs `b`, and the status of the
whole thing is the status of `b`. If `a` fails, `b` never runs, and the
status of the whole thing is the status of `a`. `a || b` is the mirror
image: `b` runs only if `a` fails.

This is called short-circuit evaluation, and it is what makes these
operators useful as control flow rather than as arithmetic. Two common
idioms show the point:

```sh
mkdir build && cd build          # only enter the directory if it was made
test -f config || echo missing   # complain only if the file is absent
```

### Inversion

`! pipeline` runs the pipeline and inverts its status: 0 becomes 1, and
anything else becomes 0. It is less common than `&&` and `||`, but
scripts need it to say "if this command fails" cleanly in Lesson 5.

### Where these came from

The Bourne shell in Seventh Edition Research UNIX already had `&&` and
`||`. The `!` operator came later and was standardized by POSIX. Plan 9's
`rc` shell, which is a different language from the one in this module,
uses the same `&&` and `||` but writes `!` in front of a command in the
same way, so the idea is shared even where the syntax is not.

---

## Design

### The grammar

The current parser has three levels, one function each. A line is
commands separated by `;` or `&`. A pipeline is commands separated by
`|`. An exec is a single command with its redirections. The new grammar
adds one level, between lines and pipelines:

```
line      ::= andor ( ( ";" | "&" ) line )?
andor     ::= pipeline ( ( "&&" | "||" ) pipeline )*
pipeline  ::= "!"? command ( "|" command )*
command   ::= exec | "(" line ")"
```

Each level of the grammar becomes one function in the parser, and each
function calls the next level down for its operands. That structure is
what gives the operators their precedence: an operator handled at a lower
level binds more tightly, because its operands are parsed first. So `|`
binds tighter than `&&` and `||`, which bind tighter than `;` and `&`.

### Precedence, worked through

Consider this line:

```sh
a | b && c ; d &
```

The parser reads it as:

1. A `line` whose first item is the and-or list `a | b && c`, followed by
   `;` and the rest, `d &`.
2. Inside the and-or list, two pipelines, `a | b` and `c`, joined by `&&`.
3. The final `d &` is a line with a single and-or list run in the
   background.

The tree has a `LIST` node at the top, an `AND` node on its left, and a
`BACK` node on its right. Students should draw this tree by hand before
writing any code, and then compare it with what the parser builds. Lesson
6 adds `sh -T`, which prints the tree, but for now a drawing on paper is
the test.

### `&&` and `||` have equal precedence

In C, `&&` binds tighter than `||`, so `a || b && c` means `a || (b &&
c)`. In the shell, the two operators have **equal precedence** and group
left to right, so the same line means `(a || b) && c`.

The difference matters. Suppose `a` succeeds and `c` fails:

- In the shell, `a` succeeds, so `b` is skipped and `(a || b)` succeeds;
  then `c` runs and fails, so the whole line fails.
- In C's grouping, `a` succeeds, so the right side `(b && c)` is skipped
  entirely and the whole expression is true.

This is one of the best examples in the module of a grammar deciding
meaning, and it is worth a full discussion in class. The equal precedence
comes straight from the grammar above, where `&&` and `||` appear in the
same rule.

### New node types

The parse tree gains three node types:

| node | fields | `eval` returns |
|---|---|---|
| `AND` | left, right | `eval(left)`, and if that is 0, `eval(right)` |
| `OR` | left, right | `eval(left)`, and if that is not 0, `eval(right)` |
| `NOT` | the pipeline | 1 if `eval(pipeline)` is 0, otherwise 0 |

None of them forks. The commands inside them fork as usual when they run
external programs, but the decision about whether to run the right side
happens in the shell's own process, which is only possible because the
evaluator from Lesson 2 returns a status.

### The status of a pipeline

The status of `a | b` is the status of the last command, `b`. That is the
traditional rule, and it means `false | true` succeeds. Modern shells
offer an option called `pipefail` that reports failure if any command in
the pipeline failed; it is an exercise below.

### Background lists

`a && b &` runs the whole and-or list in the background, not just `b`.
That falls out of the grammar: `&` belongs to the `line` level, and its
operand is the entire and-or list to its left. In the evaluator, the
`BACK` node forks one child, and the child evaluates the `AND` node
inside it. So the decision about `b` is made in the background child,
which is what a user expects.

### Tokenizing two-character operators

The tokenizer currently treats `&` and `|` as single characters. It must
now look one character ahead: `&&` and `||` are tokens of their own. The
existing tokenizer already does this for `>>`, which it turns into the
single token `+`, so students have a model to follow. The tokenizer must
also recognize `!` as an operator, but only when it starts a pipeline, so
that `echo hello!` still prints an exclamation mark.

---

## Walkthrough

### Step 1: write the tests

Tests for this lesson include:

- `true && echo yes` prints `yes`.
- `false && echo yes` prints nothing, and `echo $?` afterwards prints 1.
- `false || echo no` prints `no`.
- `true || echo no` prints nothing.
- `true || false && echo c` prints `c`, proving left-to-right grouping.
- `false && true || echo d` prints `d`.
- `! false && echo inverted` prints `inverted`.
- `false | true; echo $?` prints 0, the pipeline rule.
- `echo hello!` prints `hello!`.
- `false && echo a &` prints nothing, and the shell does not wait for it.

### Step 2: extend the tokenizer

Add the two-character operators and `!`, following the existing code for
`>>`. Write a tiny test that prints the tokens of a line, so the tokenizer
can be checked on its own before the parser changes.

### Step 3: add the new grammar level

Write `parseandor` between `parseline` and `parsepipe`. Change
`parseline` to call it. Add the optional `!` to `parsepipe`. Add the three
node types, their constructors and their cases in `nulterminate`, the
function that adds string terminators after parsing.

### Step 4: evaluate the new nodes

Add the three cases to `eval`. They are each about five lines.

---

## Lab

**Assignment.** Add `&&`, `||` and `!` with the grammar and semantics
above.

**Done when:**

- All the tests above pass, along with every earlier test.
- A hand-drawn parse tree for `a | b && c || d ; e &` is handed in and
  matches the parser's behavior.
- A paragraph explains, with an example, why `a || b && c` differs between
  C and the shell.

---

## Pitfalls

- **Giving `&&` higher precedence than `||`.** Students who know C often
  write two functions, one per operator. The test `true || false && echo
  c` then prints nothing, because the line is parsed as C would parse it.
- **Treating `&` in `&&` as a background operator.** If the tokenizer does
  not look ahead, `a && b` is read as `a &` followed by `& b`, which fails
  to parse in confusing ways.
- **Making `!` a word.** If `!` is tokenized as an ordinary word, `! cmd`
  tries to run a program called `!`.
- **Forgetting `nulterminate`.** New node types must be added there, or
  their words are not terminated and later commands see garbage.
- **Forking for `AND` or `OR`.** The nodes must not fork. If they do, the
  shell loses any state changed by a builtin on the left, such as `cd x
  && ls`.

---

## Exercises

1. **Easy.** Write a one-line script that creates a directory only if it
   does not exist, using `||` and the `test` program from Lesson 5's
   preview, or by checking `ls` output if `test` is not written yet.
2. **Easy.** What does `false; ! false; echo $?` print, and why?
3. **Medium.** Add a `pipefail` option: when it is set, a pipeline's
   status is that of the last command that failed, or 0 if none did. What
   must the evaluator remember while running a pipeline?
4. **Medium.** Explain, with a tree diagram, how the shell parses
   `a && b | c`. Then explain why `a && (b | c)` and `(a && b) | c`
   behave differently.
5. **Hard.** Write a grammar in which `&&` binds tighter than `||`, as in
   C, and change the parser to follow it. Run the tests and list which
   ones change. Then argue which design is easier for script writers.
6. **Hard.** Some shells let `!` appear only at the start of a pipeline,
   and others allow `! !` to invert twice. Decide what this shell should
   do, implement it, and justify the choice.

---

## Check your understanding

1. Why does the shell treat status 0 as true?
2. When does `b` run in `a && b`, and in `a || b`?
3. What is the status of `false | true`, and why?
4. How does the grammar give `|` higher precedence than `&&`?
5. What does `true || false && echo c` print in the shell, and what would
   the same grouping give in C?
6. Why must the `AND` node not fork?
7. In `a && b &`, which commands run in the background?
8. Why does `echo hello!` still print an exclamation mark?

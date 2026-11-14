# spec/canonical-json

This contract defines canonical JSON, which is one exact text for each JSON value. The contract is at version 1.

Two other contracts depend on it. The signature of a [policy bundle](../policy/README.md#the-signed-bundle) covers the canonical JSON of the bundle. The hash of an [audit record](../audit/README.md#the-hash-of-a-record) covers the canonical JSON of nine fields of the record. An implementation that differs in one byte fails to verify a signature or a hash that the reference made.

The contract has no schema, because it defines text and not a shape.

## The input

The input is a JSON text. Parse it with these rules.

- A number is an IEEE 754 double. A number that a double cannot hold exactly becomes the nearest double. A number beyond the range of a double becomes infinity or negative infinity.
- A string is a sequence of UTF-16 code units. An escape such as `\ud800` that is half of a surrogate pair stays in the string as it is.
- If an object has the same key twice, the later member replaces the earlier one.

## The output

The output has no white space outside strings.

| Value | Output |
|---|---|
| `null`, `true`, `false` | The same word. |
| Array | `[`, the elements in their order, separated by `,`, and `]`. |
| Object | `{`, the members separated by `,`, and `}`. Each member is the key as a string, `:`, and the value. The members are sorted by key. |
| String | See [Strings](#strings). |
| Number | See [Numbers](#numbers). |

### The order of keys

Keys are compared as sequences of UTF-16 code units, one unit at a time. A key that is a prefix of another key comes first.

The order is not the order of code points and not the order of UTF-8 bytes. The character U+1F600 is the two code units `D83D DE00`, so it comes before U+FF5E. In UTF-8, and by code point, U+FF5E would come first.

### Strings

A string starts and ends with `"`. Between them, each code unit is written as follows.

| Code unit | Output |
|---|---|
| `"` | `\"` |
| `\` | `\\` |
| U+0008, U+0009, U+000A, U+000C, U+000D | `\b`, `\t`, `\n`, `\f`, `\r` |
| Any other code unit below U+0020 | `\u` and four hexadecimal digits in lowercase, such as `\u001f` |
| A surrogate that is not half of a valid pair | `\u` and four hexadecimal digits in lowercase, such as `\ud800` |
| Everything else | The character itself |

"Everything else" includes `/`, `<`, `>`, `&`, U+007F, U+2028, U+2029, and every character outside ASCII. A valid surrogate pair is written as its one character.

### Numbers

A number is written as ECMAScript writes it (`Number::toString` in ECMA-262): the shortest decimal digits that give the same double back.

- A number with no fraction has no decimal point: `1.0` becomes `1`.
- Negative zero becomes `0`.
- A number whose absolute value is 1e21 or more has an exponent with a sign: `1e+21` and `-1e+21`. Below that, an integer is written in full.
- A number other than zero whose absolute value is below 1e-6 has an exponent: `1e-7` and `-1e-7`. From 1e-6 up, a fraction is written in full: `0.000001`.
- Infinity and negative infinity become `null`.

### Bytes

Where another contract hashes or signs canonical JSON, it uses the UTF-8 bytes of the output. The output never holds an unpaired surrogate as a raw character, so the encoding is always possible.

## Where common JSON encoders differ

A general JSON encoder often does one of these things, and each one changes the bytes.

- It escapes `<`, `>`, `&`, U+2028, or U+2029.
- It sorts keys by UTF-8 bytes or by code point, or it does not sort them.
- It writes `1.0`, `1E+21`, `1e21`, or `-0`.
- It replaces an unpaired surrogate with U+FFFD when it reads the input.
- It keeps an integer above 2^53 exact.

## Compatibility

Every byte of the output must stay the same as the output of the reference. A change to one rule on this page breaks every bundle signature and every audit hash that exists, so it needs a new version of this contract and of the two contracts that depend on it.

## Generated and authored parts

The reference is at commit `7d80045a`.

| Part | Source |
|---|---|
| The rules on this page | Authored from `apps/hub/src/policy/canonical.ts` and from the behavior of `JSON.parse` and `JSON.stringify` in ECMA-262. |

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| The input is a JSON text. The function of the reference takes a JavaScript value. It drops an object member whose value is `undefined`, writes an `undefined` array element as `null`, and fails on a value that JSON cannot hold. | A JSON text cannot hold `undefined`, so these cases cannot occur in data that another language reads. |

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **A number beyond the range of a double is written as `null`.** `1e400` becomes `null`, so two different inputs have the same canonical form.
2. **An integer above 2^53 is rounded.** `9007199254740993` becomes `9007199254740992`. A hash or a signature covers the rounded value, not the value in the input.
3. **A repeated key is accepted.** The later member wins without an error.
4. **The rules look the same as RFC 8785 (JSON Canonicalization Scheme) for every input that RFC 8785 accepts.** Nothing in this repository tests that. If the owner adopts RFC 8785 by name, an implementation can use an existing library, and the cases that RFC 8785 rejects, such as an unpaired surrogate, need a decision.

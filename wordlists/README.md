# Passphrase word list

`eff_large.txt` contains the 7,776 entries from the Electronic Frontier
Foundation's [Long Wordlist](https://www.eff.org/files/2016/07/18/eff_large_wordlist.txt),
created by Joseph Bonneau for [EFF's passphrase project](https://www.eff.org/dice).
The dice-roll numbers were removed; the words and their order are unchanged.

Distributed under [Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/),
as specified by [EFF's copyright policy](https://www.eff.org/copyright).

The application embeds this list and independently selects four words using
Go's cryptographic random number generator. The four entries with internal
hyphens are excluded from generation, and the four chosen words are separated
by hyphens.

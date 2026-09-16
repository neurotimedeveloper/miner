# miner — design decisions

The spec asks for six decisions to be made explicitly, each with its cost
stated. They are here, followed by what the implementation guarantees, what it
measures about itself, and what it does not do.

The method in one paragraph: every 50 ms of audio becomes a 32-band log-mel
frame with its gain removed and its slow spectral shape removed; one-second
windows of frames are indexed by locality-sensitive hashing; a candidate pair is
**verified** by correlating the two stretches frame by frame along their lag and
**grown** in both directions until the audio stops agreeing; the boundaries every
verified match reports cut the broadcast into atomic segments, and a match
unites each segment on its one side with the segment at the same position on
its other; a cluster is a set of segments that are transitively the same audio.

---

## 1. Repeat-detection method (non-fingerprinting)

**Decision. Dense spectral frames, an approximate index over windows of them,
and pairwise verification by correlation.**

A fingerprint keeps a sparse set of spectral peaks per second and hashes pairs
of them; content with no strong peaks - a voice-over on a music bed, a quiet
read - yields few hashes and is under-represented from the start. This keeps the
*whole* spectrum every 50 ms: 32 mel bands, 100-3800 Hz, log power, at 8 kHz.
Two normalisations make a frame describe shape rather than level:

- the frame's own mean across bands is removed, so a spot played 6 dB louder
  has the same frame;
- each band's running mean over ±0.75 s is removed (cepstral mean
  normalisation), so the spectral shape the channel, the voice and the
  processing chain impose on *everything* is gone, and what remains is the part
  of the spectrum that is moving.

The second one was decisive. Without it, two unrelated frames of one station's
output agreed 60 % of the time on the synthetic broadcast; with it, 24 %, while
the same audio still agreed at 69 % or better (5th percentile over half-second
windows).

**The analysis frame is 512 ms long, hopped every 50 ms** - ten 64 ms power
spectra averaged per stored frame (Welch's method), which integrates over the
same span as a 4096-point transform at an eightieth of the cost, and the mel
bands keep nothing a finer transform would add. Measured on two real captures of
one hour of araz_fm through different encoders and processing chains, aligned by
their true offset:

| analysis frame | same audio, 5th pct | different audio, 95th pct | share of the hour agreeing |
|---|---|---|---|
| 64 ms | 0.00 | 0.17 | 17 % |
| 128 ms | 0.04 | 0.17 | 33 % |
| 256 ms | 0.27 | 0.19 | 65 % |
| **512 ms** | **0.59** | **0.23** | **98 %** |

A short frame is defeated by sub-frame misalignment - two airings' lag is never a
whole number of frames - and by fast compressor dynamics. A long one averages
both away. Its cost is edge precision: a boundary is known to about a quarter of
a second, which is what the clustering tolerance is set to.

**Verification is what a fingerprint pipeline does not have.** The index only
says "these two seconds look alike"; whether they are the same audio, and how
far the sameness extends, is measured per frame along the lag, over a sliding
half-second window, growing outward from the seed until the score falls under
the low threshold for longer than the dip tolerance. That growth is where the
boundaries come from (decision 4) and where "the same" is decided (decision 3).

**A short match must be a strong one.** A long stretch of agreement is its
own evidence - different audio does not agree for twenty seconds - but three
seconds of it happened by chance once in a forty-minute synthetic programme.
Matches shorter than twice the minimum length must average 0.70, not 0.60.

**What is sacrificed.** Compute: one frequency transform per frame for the
query stream, and a correlation walk per candidate pair. A month of one channel
is tens of minutes, decode-bound. And repeats under about three seconds sit
below the method's floor - an edge frame is half something else - which is fine
for spots and loses the very shortest idents.

Implementation: `features.go`, `index.go`, `verify.go`.

## 2. Fitting into 10 GB for a month and scaling to two

**Decision. Keep only features, one descriptor per half second, and a sorted
index; stream everything else.**

Per hour of audio, what is held:

| what | size per hour | one month | two months |
|---|---|---|---|
| frames: 72 000 × (32 int8 + 1 float32 norm) | 2.6 MB | 1.9 GB | 3.7 GB |
| window descriptors: 7 200 × 32 float32 | 0.9 MB | 0.66 GB | 1.3 GB |
| index: 7 200 × 8 tables × 8 bytes | 0.5 MB | 0.33 GB | 0.66 GB |
| matches, segments, clusters | negligible | | |
| **total** | **4 MB** | **~2.9 GB** | **~5.7 GB** |

Measured: four real hours ran at a peak RSS of 55 MB. PCM never lives in memory
- it is streamed from ffmpeg and turned into frames as it arrives - and the
query descriptors are computed on the fly and discarded. Growth is linear in the
window length by construction; nothing in the run holds anything proportional to
the *square* of it, because the index is queried, not compared all-to-all.

**Why the index is queried at every frame but built at every tenth.** The
descriptor is phase-sensitive: a window of the same audio starting two frames
later sits as far from the first as a window of different audio does. Two
airings of one spot are aligned to within ±5 frames, so only one query phase in
ten lines up with an index built every ten frames. The index stays at one
window per half second - that is the memory - and the query runs at every frame
- that is time, one frequency row per frame. Measured: recall on the synthetic
broadcast went from 67 % to 100 %.

**What is sacrificed.** Two months at 5.7 GB is over half the ceiling. The
first reserve is quantising the descriptors to int8 (−1 GB at two months); the
second is dropping the per-frame norm (−0.4 GB). Neither is needed to meet the
criterion and neither has been spent.

Implementation: `miner.go`, `timeline.go`, `index.go` (`descriptorStream`).

## 3. The same cluster: similarity threshold, and not splitting or merging

**Decision. Two airings are the same audio when a half-second window of their
frames agrees at cosine 0.60 or better, and agreement holds - with lapses no
longer than 0.6 s and never under 0.40 - for at least three seconds.**

The thresholds are measured, not chosen: on real cross-encoder audio the same
audio's 5th percentile is 0.59 and different audio's 95th is 0.23 (decision 1's
table). 0.60 sits at the bottom of "same"; 0.40 sits well above "different".
The two-threshold hysteresis is what lets a spot survive a beat of silence or a
burst of codec noise without letting different audio in.

**Not splitting one spot.** Three things would fragment a spot and each is
handled:

- *Different gain* - removed at the frame (decision 1).
- *Trimmed edges* - a match grows only over what both airings hold, so a
  trimmed airing yields a shorter match; the boundary evidence (decision 4)
  puts the cut where the trim is, and the trimmed sliver falls below the
  minimum length and is dropped rather than becoming a cluster of its own.
- *A lapse inside one pair* - one match ending early inside the spot would cut
  every airing of it in two, and the halves would then have different
  neighbour counts and never rejoin. A cut strictly inside other matches is
  kept only when at least two **distinct** matches vote for it, with votes
  carried across every match that spans them (decision 4).

**Not counting a chorus as an airing.** Music repeats itself *inside* a
track, and every such repeat is genuine - just not an airing. On the first real
day of a music station, 448 of 557 "spots" were two stretches less than five
minutes apart: choruses. Two stretches closer than `MinLagSec` (default five
minutes) are never matched; a spot's airings are linked through the ones hours
apart regardless. The song itself, played twice a day, is one long match at one
lag - and its chorus also matches *across* the two plays at that lag plus a
chorus period. Those matches are real and they are the song's own structure;
left in, they cut both plays at every chorus and the verses come out as
thirty-second clusters of two airings an hour apart, which is exactly what a
spot looks like. A match whose both sides lie inside a match half again as
long, at a different lag, is dropped as that longer repeat's internal
periodicity. Measured on the day: 1299 clusters and 557 "spots" became 216 and
78, and the eleven songs played twice came out whole, at 140-209 s.

**Not splitting on a dropout.** One airing of a four-minute song on the real
month had lost 1.3 s to a stream dropout. Every match between that airing and
the other three broke at the dropout into a 104 s and a 145 s match at lags
1.3 s apart - three matches, and so three "distinct" votes for a cut at 104 s,
which cut every airing of the song in two and made the 104 s piece a spot.
The votes were not independent: all three came from the one damaged airing,
and what shows it is the *continuation* - the same pair of airings agrees
again within five seconds at a lag shifted by no more than the hole. Two such
matches are bridged into one that carries both lags (`bridgeDropouts`), and
the junction is a boundary nobody voted for. The same shape would split a
spot on the month one of its airings had a dropout.

**Not splitting on a soft edge, or a starved match.** After clustering, the
clusters are checked against each other the way the broadcast was: one
reference airing per cluster, laid on a timeline with a break between each,
mined once more. Two references that agree over 80 % of the *longer* one are
the same audio and their clusters are joined, the smaller cluster's airings
re-placed against the larger one's reference. This catches what the atomic
segments miss for reasons that are individually rare and collectively not: an
element whose fade-out puts its end anywhere within a second and a half came
out as twelve clusters of 8-10 s, because segments whose ends differ by more
than the half-second tolerance never unite; a spot's long-tagged version that
aired in one week matched only long airings under the per-frame cap and kept
its own cluster. Measured on the real month before the fix: 287 of 6 813 spot
clusters were wholly the same audio as another. The share is of the longer
reference on purpose: measured against the shorter, a 3 s stinger absorbed
every spot it sat inside, and a 44 s spot swallowed its own 26 s cut-down.
Cost: one more mining pass over about 80 hours of excerpts, under a minute.

**Not merging two similar spots.** Similar is not the same: two spots from one
campaign share a voice and a bed but not their frames, and the per-frame
correlation over half a second separates them where a fingerprint's sparse
peaks might not. Two spots that share a *tail* - the same three-second sting -
are the harder case: the tail is a genuine repeat. It becomes an atomic
segment of its own, uniting with every other airing of the tail, and the two
spots' cores unite only with their own airings. `TestASharedTailDoesNotMergeTwoSpots`
pins it.

**What is sacrificed.** A spot re-cut by the agency - the same audio with a
different last second - is two clusters, one per version, and that is the
right answer. A spot whose two airings differ by more than the thresholds allow
(a heavily re-processed rebroadcast) is not found as one; the thresholds can be
moved, at the cost of letting different audio in.

Implementation: `verify.go`, `cluster.go`, `merge.go`; defaults in `options.go`.
Tests: `TestGainDifferencesDoNotSplitASpot`, `TestASharedTailDoesNotMergeTwoSpots`,
`TestBoundaryVotesDropLoneCutsAndCarryAgreedOnes`,
`TestADropoutInOneAiringDoesNotCutTheOthers`; `TestFragmentation` re-measures a
finished month against its own audio.

## 4. Segment boundaries when the surrounding content differs

**Decision. A boundary is where matches end, and the broadcast is cut at every
boundary any match reports before anything is clustered.**

When two airings of a spot sit in different programme, the match between them
ends where the spot ends - that is what growing until the audio stops agreeing
means - and both airings report the same two cuts. That much a fingerprint
pipeline can also do. What it cannot do is the commonest real case: two spots
that usually air back to back. Matches between two *paired* airings cover both
spots as one stretch; matches against a *solo* airing cover one. A clustering
that treated each matched interval as an occurrence split one spot's airings
into a "paired" cluster and a "solo" cluster.

So the timeline is first cut, at every boundary any match reports, into atomic
segments. A match unites each atomic segment on its A side with the segment at
the same position on its B side. A paired airing is then two segments, each
joining its own spot's cluster, and the pair is not a cluster at all.

Two refinements, both forced by measurement:

- **Boundaries need support.** A single match ending early inside a spot
  reports a cut nobody else sees. A cut strictly inside other matches needs two
  distinct matches behind it, and a cut's support travels along every match
  that spans it: a spot that airs alone once and three times before the same
  song ends, on its solo airing, at a cut three matches agree on, and that
  evidence is carried to the paired airings where the same cut is one match's
  word.
- **Always-adjacent clusters are joined** - two segments that never air apart
  are one repeat, either because a spurious cut split it or because two spots
  have never aired separately, which the broadcast cannot tell apart. Except
  when one of them is longer than a spot: a song that always follows a spot is
  still a song, and joining them would hand the spot the song's verdict.

**Airings are then re-placed against the cluster's reference.** A segment's
edges are exact to about half a second; the airing's true position is exact to
a frame, one lag refinement away, and every airing carries the reference's
length - the segment *is* one thing. On the first real day, before this step
an audit that aligned airings to within ±3 frames flagged 28 of 216 clusters;
after it, alignment within a frame is the reported position.

**What is sacrificed.** Precision at the cut: about a quarter of a second, the
analysis frame's half-width. And the ambiguity is real - two spots that have
*never* aired apart in two months are reported as one, because nothing in the
broadcast says otherwise. The moment one of them airs alone, they separate.

Implementation: `cluster.go`.
Tests: `TestPairedSpotsDoNotFragment`, `TestASongIsANonSpotAndSwallowsNothing`,
`TestAlwaysAdjacentClustersAreJoinedUnlessOneIsLong`.

## 5. Spot versus jingle, stinger, music

**Decision. Rules on how a repeat behaves, each verdict carrying its reason.**

Nothing in the audio says "advertisement". The broadcast does show how a
repeat behaves, and that is what is used, in this order:

1. **A piece of a unit longer than 120 s** is not a spot. Clusters whose
   airings follow each other in the same order in 80 % of cases, both ways,
   are pieces of one thing; a four-minute song that came out as 104 s + 145 s
   is still a song, though its first piece is spot-length.
2. **Under 8 s** is not a spot: an ident, a stinger, a sound effect.
3. **Over 120 s** is not a spot: a song, a programme element.
4. **At the same minute of the hour** in 80 % or more of its airings (given at
   least four) is not a spot: a news sting, an hourly ident, a scheduled
   element - a sold slot lands where the sales house sold it.
5. Otherwise it is a spot. The reason records whether its airings are
   *accompanied* - another repeat within two seconds before or after, which is
   what an advertising block looks like - or alone.

Every cluster's `Reason` is one sentence a person can argue with, because a
flag that cannot be argued with cannot be corrected either.

**What is sacrificed.** These are the numbers most worth retuning against the
real list, and they have been tuned only against synthetic material and one
real hour. A 6-second spot is called an ident; a 130-second infomercial a
programme element; a spot the station itself runs at the top of every hour, a
scheduled element. The rules are in one function with the thresholds named at
its head. Speech-versus-music inside the repeat would sharpen rule 4 and is the
first thing to add when the list shows it is needed.

Implementation: `classify.go`. Test: `TestClassifyReasonsByDurationRegularityAndCompany`.

## 6. Determinism with a stochastic component

**Decision. The one stochastic step is drawn once from a fixed seed, and
everything after it is a total order.**

The index's hyperplanes are random normals from `math/rand` seeded with
`Options.Seed` (default 1). The same seed gives the same tables, the same
candidates in the same order, and the same clusters; a different seed gives a
different but equally valid index. Nothing else in the method draws a random
number.

Everything that *could* vary between runs is pinned: files are processed in
`Start` order however they were decoded; candidates are sorted and deduplicated;
union-find takes the lower index as root; clusters are ordered by first airing
then length; no map is iterated where its order could reach the output.

Measured: `TestAcceptanceDeterminism` runs the same broadcast twice and compares
every cluster's every airing.

**What is sacrificed.** A seed is one more thing a caller has to keep constant
between runs that are meant to be comparable. It is exposed as a flag and
defaults to a fixed value so that forgetting it changes nothing.

---

## Input shapes met on real material

- **The recorder writes 61-minute files every hour.** Consecutive files overlap
  by a minute; kept twice, that minute is a repeat of itself at every hour of
  the month. The previous file's overlap is dropped and the join is continuous.
- **A day is a directory.** Inputs are walked recursively.

## Guarantees

- A corrupt or unreadable file is skipped with a reason and the run continues
  (`TestAcceptanceCorruptFileIsSkipped`). The scratch directory is removed
  whatever happens.
- Every ffmpeg/ffprobe call runs under a timeout in its own process group, and
  the group is killed on timeout or cancellation.
- Memory is linear in the audio length, with no PCM held (decision 2).
- The same input and seed give the same clusters (decision 6).
- Every occurrence is located in broadcast time to the analysis hop (50 ms)
  and in its source file by offset, and every cluster's representative is cut
  from the airing closest to the cluster's consensus length.

## What was measured

On the synthetic broadcast (40 minutes, six spots and two idents aired five
times each at gains from −8 to +3 dB, edges trimmed by up to half a second,
half of them in blocks, across a file join): recall 40/40, one cluster per
repeat, every flag right, no cluster outside the truth, durations within 0.3 s.
The harder shapes - paired spots, a shared tail, gains from −18 to +4 dB, a song
that follows a spot - each have their own test and pass.

On one real hour of araz_fm: 61 repeating clusters in 7.8 s at 25 MB, among them
seventeen spot-length repeats at plausible lengths (8-30 s) airing at what look
like block times, and a 6.3 s ident airing seven times.

On four captures of that hour through four different chains, laid end to end
as four hours: 89 clusters found in all four captures, where the same run with
64 ms frames found most repeats in only two.

## Non-guarantees, plainly

- **Recall against a real spot list has not been measured.** The synthetic
  numbers are for the method; the channel's number needs the list.
- **Repeats under about three seconds** are below the floor.
- **A spot never aired apart from its neighbour** is reported joined to it.
- **A rebroadcast programme** is a repeat and is reported as one, flagged by
  length as not a spot; nothing more is known about it.

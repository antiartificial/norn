# ConcurrentAdmission

TLC reached 12 states and 16 steps. The chart draws 12 states and 14 arrows.
The chart does not draw 2 steps that stay in one chart state.

## SameKeyConverges breaks
The shortest path has 5 steps:
0. resultA="none" resultB="none" a="initial" b="initial" committed=FALSE
1. MissA: resultA="none" resultB="none" a="prepared" b="initial" committed=FALSE
2. CommitA: resultA="none" resultB="none" a="accepted" b="initial" committed=TRUE
3. MissB: resultA="none" resultB="none" a="accepted" b="prepared" committed=TRUE
4. FenceLoseB: resultA="none" resultB="none" a="accepted" b="failed" committed=TRUE
5. Finish: resultA="fresh" resultB="error" a="accepted" b="failed" committed=TRUE

## Rules that hold
TypeOK, AtMostOneFresh

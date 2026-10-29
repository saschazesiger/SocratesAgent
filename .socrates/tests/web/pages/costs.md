---
title: Costs shows the spending per period, filter and group, readable on the device
priority: P1
tags: [pages, costs, layout]
---
The real agents of the other cells spend a few cents, so Costs may be empty at first and fill during
the run; either is fine. With "No costs yet" there are no chips or filters: steps 2 to 4 are then `na`.

## Steps

1. Sign in and open Costs from the session list.
   Expect: the heading with its explanation ("What every conversation on this machine would cost at the published API prices."), the period chips (7 days, 30 days, 90 days, This month, Last month, This year, All) and the filters; or the empty state "No costs yet".
2. Choose All, then 7 days.
   Expect: the figures and the chart follow the period; an empty period says "Nothing was spent in this period".
3. Scroll through the page: Cost per day, the groupings (By program, By model, By account, By project) and Most expensive conversations.
   Expect: charts and tables fit the screen (tables stack or scroll inside themselves on a phone); no sideways page scroll; numbers readable.
4. Use a filter (All programs: pick one), then Clear filters.
   Expect: the page narrows to it and comes back.
5. Go back to the app.
   Expect: the app opens again on Home.

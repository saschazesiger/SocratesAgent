---
title: The session list, the top bar and the pages it leads to
priority: P0
tags: [smoke, navigation, layout]
---
How a person moves around: the session list (a modal drawer up to 900 px wide, docked on wider
screens), the top bar, and the links to Costs, Tests and Admin.

## Steps

1. Sign in, then open the session list (on a phone or narrow tablet: the Sessions button; on a wide screen it is already there).
   Expect: New session, Home, the sessions grouped by folder ("demo" with the seeds), and at the bottom Costs, Tests, Admin and Sign out; on a phone the drawer covers part of the page over a dimmed scrim.
2. Close the drawer by tapping the scrim (phones and narrow tablets); on a wide screen press Hide the session list, then Show the session list.
   Expect: the drawer closes (or the list collapses and comes back) without the page jumping; nothing stays dimmed.
3. Open "Seed: tools and a diff" from the list.
   Expect: the session opens; the top bar shows its title and the ⋮ button (a session in progress shows no status chip); on a phone the drawer closed by itself.
4. Collapse the "demo" folder in the list, then open it again.
   Expect: the folder's sessions hide and come back; the folder's head stays in place.
5. Open Costs from the list, then go back with the browser's Back.
   Expect: the Costs page opens with its heading; Back returns to the app.
6. Open Tests from the list, then Admin, each time coming back to Home through the page's way back (its link or the browser's Back).
   Expect: both pages open with their own heading and layout and none of them scrolls sideways; the way back to the app works.
7. On a phone or tablet: rotate to landscape with the drawer open, then back to portrait (na on desktops).
   Expect: the drawer and the page follow the rotation; the footer may turn into icons on a short screen; nothing is lost or overlaps.

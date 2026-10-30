---
title: Notifications can be turned on where the browser allows it, and the app is installable
priority: P2
tags: [pages, pwa, notifications]
---
Socrates is a web app with a manifest and a service worker (it runs over HTTPS here). Notifications
need the browser's permission: answer the browser's prompt with `qa prompt allow` on phones and
tablets. A desktop's prompt is out of reach: on Windows Chrome start the device with
`qa start --grant notifications` (the browser then asks nothing; say so); on the Mac's Safari, whose
prompt cannot be answered, the step is `blocked`, not `fail`. On iPhone and iPad they need the app
added to the Home Screen (the hint says so).

## Steps

1. Sign in and open /static/manifest.json.
   Expect: the manifest (JSON) with the name Socrates, display standalone and the shortcuts Questions and Home.
2. Open / again, open the options for this device (⋮ at the top right) and read Notifications.
   Expect: the hint fits the device: "When a session finishes or needs an answer", "Add to Home Screen for notifications" or "Needs iOS 16.4 or later" on iOS, or "Not available in this browser".
3. Where the switch is offered, switch Notifications on.
   Expect: the browser asks for permission (blocked if the prompt cannot be answered); after allowing, the switch stays on without an error.
4. Switch it off again and close with Done.
   Expect: the switch is off; no error.

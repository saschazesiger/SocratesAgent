---
title: browser.lol web app (apps/web-browser)
tools: [browserstack]
columns: device
dimensions:
  device:
    values:                    # the matrix, in the order of the table's columns
      iphone: iPhone 15 · Safari
      iphone-old: iPhone 13 · iOS 16
      ipad: iPad 10th · Safari
      pixel: Pixel 9 · Chrome
      moto: Moto G71 · Chrome
      samsung: Galaxy S25 · Samsung Internet
      win-chrome: Windows · Chrome
      win-firefox: Windows · Firefox
      mac-safari: macOS · Safari
    extra:                     # run only by a test that names them
      win-edge: Windows · Edge
      iphone-se-ios15: iPhone SE · iOS 15
    default: all
  locale:
    values: {en: English, de: German, fr: French, es: Spanish, it: Italian, pt: Portuguese, ja: Japanese}
    default: rotate
params:
  consent: accept
evidence: screenshot
timeout: 45m
---
## Working a cell

Start the device with `qa start`, work the steps in order and judge each one on its screenshot before the next.
An ad covering a control is an issue of the layout; a Google or Microsoft challenge is blocked, not failed.
The device's own English validation bubbles are not a finding.

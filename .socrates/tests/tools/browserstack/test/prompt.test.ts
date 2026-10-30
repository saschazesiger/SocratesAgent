import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { answerButton, describePrompt, findNativePrompt, xpathLiteral } from "../src/prompt";

const fixture = (name: string) => readFileSync(new URL(`fixtures/${name}`, import.meta.url), "utf8");

test("finds Chrome's clipboard question on a Pixel", () => {
  const prompt = findNativePrompt(fixture("android-chrome-clipboard-prompt.xml"));
  assert.deepEqual(prompt, { texts: ["socrates-qa.zesiger.net wants to see text and images copied to the clipboard"], buttons: ["Block", "Allow"] });
  assert.equal(describePrompt(prompt!), '"socrates-qa.zesiger.net wants to see text and images copied to the clipboard" [Block] [Allow]');
});

test("the page's own buttons, the toolbar and the keyboard are no prompt", () => {
  const xml = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy">
  <android.widget.FrameLayout package="com.android.chrome" class="android.widget.FrameLayout" text="">
    <android.widget.EditText package="com.android.chrome" class="android.widget.EditText" text="socrates-qa.zesiger.net/#home" resource-id="com.android.chrome:id/url_bar" />
    <android.webkit.WebView package="com.android.chrome" class="android.webkit.WebView" text="Socrates">
      <android.view.View package="com.android.chrome" class="android.view.View" text="">
        <android.widget.Button package="com.android.chrome" class="android.widget.Button" text="Sign in" />
        <android.widget.TextView package="com.android.chrome" class="android.widget.TextView" text="Welcome &amp; hello" />
      </android.view.View>
    </android.webkit.WebView>
  </android.widget.FrameLayout>
  <android.widget.FrameLayout package="com.google.android.inputmethod.latin" class="android.widget.FrameLayout" text="">
    <android.widget.Button package="com.google.android.inputmethod.latin" class="android.widget.Button" text="Done" />
  </android.widget.FrameLayout>
</hierarchy>`;
  assert.equal(findNativePrompt(xml), null);
});

test("Android's own permission dialog counts, its entities decoded", () => {
  const xml = `<hierarchy>
  <android.widget.LinearLayout package="com.android.permissioncontroller" class="android.widget.LinearLayout" text="">
    <android.widget.TextView package="com.android.permissioncontroller" class="android.widget.TextView" text="Allow Chrome to record audio?" displayed="true" />
    <android.widget.Button package="com.android.permissioncontroller" class="android.widget.Button" text="While using the app" displayed="true" />
    <android.widget.Button package="com.android.permissioncontroller" class="android.widget.Button" text="Don&apos;t allow" displayed="true" />
    <android.widget.Button package="com.android.permissioncontroller" class="android.widget.Button" text="Hidden" displayed="false" />
  </android.widget.LinearLayout>
</hierarchy>`;
  assert.deepEqual(findNativePrompt(xml), { texts: ["Allow Chrome to record audio?"], buttons: ["While using the app", "Don't allow"] });
});

test("the answer's button, best label first", () => {
  const chrome = { texts: ["x wants to see text and images copied to the clipboard"], buttons: ["Block", "Allow"] };
  assert.equal(answerButton(chrome, "allow"), "Allow");
  assert.equal(answerButton(chrome, "block"), "Block");
  const location = { texts: ["x wants to use your device's location"], buttons: ["Allow this time", "Allow on every visit", "Don't allow"] };
  assert.equal(answerButton(location, "allow"), "Allow this time");
  assert.equal(answerButton(location, "block"), "Don't allow");
  assert.equal(answerButton({ texts: [], buttons: ["Open"] }, "allow"), null);
});

test("XPath literals keep any quote", () => {
  assert.equal(xpathLiteral("Allow"), '"Allow"');
  assert.equal(xpathLiteral('Say "hi"'), `'Say "hi"'`);
  assert.equal(xpathLiteral(`Don't "x"`), `concat("Don't ", '"', "x", '"', "")`);
});

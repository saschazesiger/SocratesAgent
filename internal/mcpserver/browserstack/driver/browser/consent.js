// Answers the ad consent dialog (Google Funding Choices) when it is open: policy accept | reject | leave.
function main(policy) {
  var root = document.querySelector(QA_CONSENT);
  if (!root || !qaVisible(root)) return { open: false };
  var dialog = root.querySelector(".fc-dialog, .fc-consent-root > div") || root;
  if (!qaVisible(dialog)) return { open: false };
  if (policy === "leave") return { open: true, action: "left open" };
  var accept = root.querySelector(".fc-cta-consent");
  var reject = root.querySelector(".fc-cta-do-not-consent");
  var button = policy === "reject" ? reject : accept;
  if (!button || !qaVisible(button)) return { open: true, action: "no " + policy + " button", buttons: Array.prototype.map.call(root.querySelectorAll("button"), qaName).slice(0, 6) };
  button.click();
  return { open: true, action: policy === "reject" ? "rejected" : "accepted", label: qaName(button) };
}

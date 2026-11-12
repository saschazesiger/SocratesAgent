/*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash | bundle "bundle" (ds.bundle.json) */
/*
 * Z-Dash bundle "bundle" (tools/bundle.mjs; do not edit): window.DS with
 * DS.core, DS.motion, DS.indicator, DS.dom, DS.ripple, DS.icons, DS.position, DS.overlay, DS.a11y, DS.format, DS.store, DS.virtual, DS.dnd, DS.badge, DS.chart, DS.composer, DS.dialog, DS.form, DS.formatted, DS.highlight, DS.code, DS.lightbox, DS.list, DS.listbox, DS.combobox, DS.menu, DS.page, DS.sheet, DS.shell, DS.toast, DS.tooltip and 276 icons.
 * Icon shapes: Lucide (ISC) and Feather (MIT), see LICENSE.
 * Another bundle or ds.js of the same version on the page adds its modules
 * to the same ZDash.versions[version] object.
 */
(function (root) {
  'use strict';
  var version = '3.0.1';
  // DashboardStyle: the deprecated name from before the rename (3.0), the same object.
  var registry = root.ZDash || root.DashboardStyle || {};
  root.ZDash = registry;
  if (!root.DashboardStyle) root.DashboardStyle = registry;
  var versions = registry.versions || (registry.versions = {});
  var first = versions[version];
  var DS = first || { version: version };
  var add = function (name, exports, make) {
    var theirs = DS[name];
    if (theirs && exports.every(function (key) { return key in theirs; })) return;
    var mine = make();
    if (!theirs) {
      DS[name] = mine;
      return;
    }
    var merged = {};
    Object.keys(theirs).forEach(function (key) { merged[key] = theirs[key]; });
    Object.keys(mine).forEach(function (key) { if (!(key in merged)) merged[key] = mine[key]; });
    DS[name] = Object.freeze(merged);
  };

  add('core', ['runtime', 'define', 'get', 'manage', 'init', 'destroy', 'emit', 'config', 't', 'safeHref'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const registry = globalThis.ZDash ?? globalThis.DashboardStyle ?? {};
    globalThis.ZDash = registry;
    globalThis.DashboardStyle ??= registry;
    function createRuntime() {
    const states = new Map();
    return Object.freeze({
    state(name, init) {
    if (!states.has(name)) states.set(name, typeof init === 'function' ? init() : init ?? {});
    return states.get(name);
    },
    });
    }
    const runtime = registry.runtime ?? createRuntimeOnce();
    function createRuntimeOnce() {
    const made = createRuntime();
    Object.defineProperty(registry, 'runtime', { value: made, enumerable: true });
    return made;
    }
    const DEFAULT_STRINGS = Object.freeze({
    close: 'Close',
    copy: 'Copy',
    copied: 'Copied',
    showMore: 'Show more',
    showLess: 'Show less',
    clear: 'Clear',
    loading: 'Loading',
    more: 'More',
    dismiss: 'Dismiss',
    required: 'Required',
    optional: 'Optional',
    ok: 'OK',
    cancel: 'Cancel',
    confirm: 'Confirm',
    actionFailed: 'That did not work. Try again.',
    valueRequired: 'Enter a value.',
    expand: 'Expand',
    collapse: 'Collapse',
    remove: 'Remove',
    previous: 'Previous',
    next: 'Next',
    noResults: 'No results',
    keyCtrl: 'Ctrl',
    keyAlt: 'Alt',
    keyShift: 'Shift',
    keyMeta: 'Win',
    keyEnter: 'Enter',
    keyEscape: 'Esc',
    keySpace: 'Space',
    keyTab: 'Tab',
    keyBackspace: 'Backspace',
    keyDelete: 'Delete',
    keyHome: 'Home',
    keyEnd: 'End',
    keyPageUp: 'Page Up',
    keyPageDown: 'Page Down',
    hotkeysGeneral: 'General',
    dragHint: 'Press Space to pick up. Then the arrow keys move it, Space drops it and Escape cancels.',
    dragGrabbed: 'Picked up {label}, position {position} of {total}.',
    dragMoved: 'Moved {label} to position {position} of {total}.',
    dragMovedZone: 'Moved {label} to {zone}, position {position} of {total}.',
    dragDropped: 'Dropped {label} at position {position} of {total}.',
    dragDroppedZone: 'Dropped {label} in {zone}, position {position} of {total}.',
    dragCancelled: 'Cancelled. {label} is back at position {position} of {total}.',
    splitpaneResize: 'Resize',
    splitpaneCollapsed: 'Collapsed',
    back: 'Back',
    moreOptionsFor: 'More options for {label}',
    moreActions: 'More actions',
    copyManual: 'Copying is blocked here. The text is selected: press {keys} to copy it.',
    decrease: 'Decrease',
    increase: 'Increase',
    showPassword: 'Show password',
    capsLock: 'Caps Lock is on.',
    strengthWeak: 'Weak',
    strengthFair: 'Fair',
    strengthGood: 'Good',
    strengthStrong: 'Strong',
    passwordStrength: 'Password strength: {level}',
    secretSet: 'Set',
    secretNotSet: 'Not set',
    secretCleared: 'Removed when you save',
    recentSearches: 'Recent searches',
    clearRecent: 'Clear recent searches',
    searchResultsOne: '{count} result',
    searchResultsMany: '{count} results',
    edit: 'Edit',
    save: 'Save',
    saved: 'Saved',
    typeToConfirm: 'Type {name} to confirm',
    errorSummaryOne: 'There is 1 problem',
    errorSummaryMany: 'There are {count} problems',
    otpDigit: 'Digit {index} of {count}',
    otpCharacter: 'Character {index} of {count}',
    charactersLeft: '{count} characters left',
    charactersLeftOne: '1 character left',
    charactersOver: '{count} characters too many',
    charactersOverOne: '1 character too many',
    errorSummaryField: '{label}: {message}',
    createOption: 'Create “{text}”',
    resultsOther: '{count} results',
    typeToSearch: 'Type to search',
    added: 'Added {label}',
    removed: 'Removed {label}',
    selectedCount: '{count} selected',
    tagDuplicate: '{label} is in the list already',
    tagInvalid: '{label} is not valid',
    tagMax: 'Up to {max} items',
    allSelected: 'All selected',
    orderPosition: 'Position {position}',
    transferMoved: 'Moved {count} to {list}',
    previousMonth: 'Previous month',
    nextMonth: 'Next month',
    selected: 'selected',
    chooseDate: 'Choose date',
    chooseDates: 'Choose dates',
    chooseTime: 'Choose time',
    empty: 'Empty',
    rangeStart: 'Start',
    rangeEnd: 'End',
    presets: 'Presets',
    lastSpan: 'Last {span}',
    timeRange: 'Time range',
    absoluteRange: 'Absolute time range',
    rangeFrom: 'From',
    rangeTo: 'To',
    apply: 'Apply',
    recentRanges: 'Recently used',
    quickRanges: 'Quick ranges',
    refresh: 'Refresh',
    autoRefresh: 'Auto refresh',
    off: 'Off',
    rangeOrder: 'The start must come before the end.',
    retry: 'Retry',
    uploadWaiting: 'Waiting',
    uploadDone: 'Uploaded',
    uploadFailed: 'Upload failed',
    uploadRetry: 'Retry {name}',
    uploadRemove: 'Remove {name}',
    uploadProgress: 'Upload of {name}',
    uploadWrongType: '{name} is not a type this field takes.',
    uploadTooMany: '{name} was not added: at most {max} files.',
    uploadTooLarge: '{name} is larger than {max}.',
    uploadEmpty: '{name} is empty.',
    uploadAdded: 'Added {name}.',
    uploadAddedCount: 'Added {count} files.',
    uploadRemoved: 'Removed {name}.',
    uploadDoneAnnounce: '{name} uploaded.',
    uploadFailedAnnounce: 'Upload of {name} failed.',
    kvAdd: 'Add row',
    kvKeyLabel: 'Key, row {row}',
    kvValueLabel: 'Value, row {row}',
    kvValueOf: 'Value of {key}',
    kvRemove: 'Remove {key}',
    kvRemoveRow: 'Remove row {row}',
    kvShow: 'Show the value of {key}',
    kvShowRow: 'Show the value of row {row}',
    kvDuplicate: '{key} is also on row {rows}: the last one counts.',
    kvNoKey: 'Add a key, or this row is left out.',
    kvRemoved: 'Removed {key}.',
    kvRemovedRow: 'Removed a row.',
    kvPasted: 'Pasted {count} rows.',
    wizardDone: 'done',
    wizardFix: 'Needs attention',
    wizardStepOf: 'Step {index} of {count}',
    wizardStep: 'Step {index} of {count}: {label}',
    wizardError: 'One field needs attention.',
    wizardErrors: '{count} fields need attention.',
    wizardEmpty: 'Not given',
    wizardEdit: 'Edit {label}',
    wizardEditShort: 'Edit',
    wizardYes: 'Yes',
    wizardNo: 'No',
    unsavedOne: '1 unsaved change',
    unsavedMany: '{count} unsaved changes',
    unsavedSaveKey: 'Save changes',
    unsavedSaved: 'Changes saved.',
    unsavedFailed: 'Not saved. Your changes are still here.',
    unsavedDiscarded: 'Changes discarded.',
    unsavedLeaveTitle: 'Leave without saving?',
    unsavedLeaveText: 'Your unsaved changes will be lost.',
    unsavedLeave: 'Discard changes',
    unsavedStay: 'Keep editing',
    unsavedChangedWord: '(changed)',
    queryIs: 'is',
    queryNot: 'is not',
    queryGt: 'over',
    queryGte: 'at least',
    queryLt: 'under',
    queryLte: 'at most',
    queryOr: 'or',
    queryText: 'Text',
    queryRemove: 'Remove filter',
    queryTokenHint: 'Backspace or Delete removes it, F2 edits it.',
    queryUnknownKey: '(not a known filter)',
    queryBadOp: '(this filter does not compare)',
    queryBadValue: '(not a valid value)',
    querySuggestions: 'Suggestions',
    queryKeys: 'Filter by',
    queryOperators: 'Compare',
    querySaved: 'Saved queries',
    querySearch: 'Search',
    querySearchFor: 'Search for "{text}"',
    queryOne: '1 suggestion',
    queryMany: '{count} suggestions',
    queryNoMatch: 'No suggestions',
    queryUnknown: 'Not a known filter',
    querySavedHint: 'Enter applies this saved query, Delete removes it.',
    queryAdded: 'Added filter {label}.',
    queryRemoved: 'Removed filter {label}.',
    queryEditing: 'Editing filter {label}.',
    queryCleared: 'Cleared the query.',
    queryApplied: 'Applied {name}.',
    queryClear: 'Clear the query',
    querySave: 'Save this query',
    querySaveTitle: 'Save the query',
    querySaveName: 'Name',
    querySaveConfirm: 'Save',
    querySavedAs: 'Saved the query as {name}.',
    querySavedRemoved: 'Removed the saved query {name}.',
    datagridSelect: 'Select',
    datagridSelectAll: 'Select all rows',
    datagridSelectRow: 'Select {label}',
    datagridSelected: '{count} selected',
    datagridSelectEvery: 'Select all {total}',
    datagridSelectedEvery: 'All {total} selected',
    datagridDetails: 'Details',
    datagridDetailsOf: 'Details of {label}',
    datagridActions: 'Actions',
    datagridSorted: 'Sorted by {column}, {direction}.',
    datagridUnsorted: 'Sorting removed.',
    datagridSortRank: ', sort {rank}, {direction}',
    datagridAscending: 'ascending',
    datagridDescending: 'descending',
    datagridResize: 'Resize {column}',
    datagridMove: 'Move {column}',
    datagridColumns: 'Columns',
    datagridPresets: 'Views',
    datagridPresetDefault: 'Default',
    datagridPresetAll: 'All columns',
    datagridPresetApplied: '{preset} shown.',
    datagridColumnShown: '{column} shown.',
    datagridColumnHidden: '{column} hidden.',
    datagridRowsOne: '{count} row',
    datagridRowsOther: '{count} rows',
    datagridPage: 'Page {page} of {pages}',
    datagridRange: '{from}–{to} of {total}',
    datagridTotal: 'Total',
    datagridNoValue: 'No value',
    datagridEmpty: 'Nothing here yet',
    datagridNoResultsText: 'Nothing matches the filter.',
    datagridError: 'The data did not load.',
    datagridRetry: 'Try again',
    datagridInvalidNumber: 'Enter a number.',
    datagridEditSaved: '{column} saved.',
    datagridEditRefused: '{column} not saved.',
    datagridCopiedOne: '{count} row copied.',
    datagridCopiedOther: '{count} rows copied.',
    sortAscending: 'Ascending',
    sortDescending: 'Descending',
    sortAscendingText: 'A to Z',
    sortDescendingText: 'Z to A',
    sortAscendingNumber: 'Lowest first',
    sortDescendingNumber: 'Highest first',
    sortAscendingDate: 'Oldest first',
    sortDescendingDate: 'Newest first',
    sortAnnounce: 'Sorted by {field}, {direction}',
    loadMoreStatus: 'Showing {loaded} of {total}',
    loadMoreStatusOpen: 'Showing {loaded}',
    loadMoreDone: 'All {total} shown',
    loadMoreLoaded: 'Loaded {count} more.',
    loadMoreLoading: 'Loading more',
    loadMoreFailed: 'Could not load more.',
    tryAgain: 'Try again',
    removeFilter: 'Remove filter',
    filterRemoved: 'Removed {filter}',
    filtersCleared: 'Filters cleared',
    filtersApplied: '{count} applied',
    filterChip: '{label}: {value}',
    resultsOne: '{count} result',
    showResults: 'Show {results}',
    rangeMin: 'from {min}',
    rangeMax: 'up to {max}',
    rangeBetween: '{min} to {max}',
    facetShowAll: 'Show all {count}',
    facetShowFewer: 'Show fewer',
    facetNoMatch: 'No match for “{query}”',
    facetMatchesOne: '{count} match',
    facetMatchesOther: '{count} matches',
    viewEdited: 'Edited',
    viewDefault: 'Default',
    viewApplied: 'View: {name}',
    viewSaved: 'Saved {name}',
    viewDeleted: 'Deleted {name}',
    viewDefaultSet: '{name} is the default view',
    viewLinkCopied: 'Link copied',
    viewNameLabel: 'Name',
    viewNewTitle: 'Save as a new view',
    viewRenameTitle: 'Rename view',
    viewSave: 'Save',
    viewLinkTitle: 'Link to this view',
    viewDeleteTitle: 'Delete “{name}”?',
    viewDeleteText: 'The view goes from this browser. The list and its data stay as they are.',
    viewDeleteConfirm: 'Delete view',
    treeMove: 'Move {label}',
    treeMoveBefore: 'Before {target}, in {parent}.',
    treeMoveAfter: 'After {target}, in {parent}.',
    treeMoveInside: 'Into {target}.',
    treeDropped: 'Moved {label} to {parent}, position {position} of {total}.',
    treeLoaded: 'Loaded {label}.',
    treeLoadFailed: 'Could not load {label}.',
    treeError: 'Could not load',
    jsonTree: 'JSON',
    jsonSearch: 'Search keys and values',
    jsonFind: 'Search',
    jsonPrevious: 'Previous match',
    jsonNext: 'Next match',
    jsonMatch: '{current} of {total}',
    jsonExpandAll: 'Expand all',
    jsonCollapseAll: 'Collapse all',
    jsonCopyValue: 'Copy the value of {label}',
    jsonCopyPath: 'Copy the path of {label}',
    jsonKey: '{count} key',
    jsonKeys: '{count} keys',
    jsonItem: '{count} item',
    jsonItems: '{count} items',
    jsonInvalid: 'Not valid JSON: line {line}, column {column}.',
    listMove: 'Move {label}',
    listSelected: '{count} selected',
    factsCopy: 'Copy {label}',
    auditlogToday: 'Today',
    auditlogYesterday: 'Yesterday',
    auditlogUndated: 'No date',
    auditlogBefore: 'Before:',
    auditlogAfter: 'After:',
    auditlogNotSet: 'Not set',
    auditlogEvent: '{count} event',
    auditlogEvents: '{count} events',
    auditlogFiltered: '{shown} of {total} events',
    auditlogMore: 'Show {count} more',
    kbdCommand: 'Command',
    kbdOption: 'Option',
    kbdControl: 'Control',
    kbdThen: 'then',
    labelGroupMore: '+{count} more, show all {total}',
    labelGroupAll: 'All labels',
    chartTableView: 'Show as table',
    chartRole: 'chart',
    chartKeys: 'Left and Right move along the points, Up and Down through the series, Page Up and Page Down jump, Home and End go to the ends.',
    chartKeysBars: 'Arrow keys move along the bars, Page Up and Page Down jump, Home and End go to the ends.',
    chartKeysSlices: 'Arrow keys move through the slices, Home and End go to the ends.',
    chartPoint: '{x}: {values}',
    chartSummary: '{name}: from {first} to {last}, lowest {min}, highest {max}',
    chartTotal: 'Total',
    chartOther: 'Other',
    chartShown: '{series} shown',
    chartHidden: '{series} hidden',
    chartEmpty: 'No data',
    moreInfo: 'More information',
    done: 'Done',
    commandOpen: 'Open the command palette',
    commandRecent: 'Recent',
    commandResults: '{count} results',
    commandResultOne: '1 result',
    commandEmpty: 'No results for "{query}"',
    commandFailed: 'Some results could not be loaded.',
    commandBack: 'Back to {page}',
    shortcutsOpen: 'Show the keyboard shortcuts',
    shortcutsGlobal: 'Everywhere',
    shortcutsOff: 'Not available here',
    shortcutsOr: 'or',
    shortcutsEmpty: 'No keyboard shortcuts here.',
    notificationsCount: '{label}, {count} unread',
    notificationsUnread: 'Unread',
    notificationsMarkRead: 'Mark as read',
    notificationsMarkUnread: 'Mark as unread',
    notificationsAllRead: 'All notifications marked as read.',
    notificationsArrived: 'New notification: {title}',
    notificationsArrivedMany: '{count} new notifications',
    notificationsToday: 'Today',
    notificationsYesterday: 'Yesterday',
    notificationsEarlier: 'Earlier',
    notificationsEmpty: 'No notifications',
    notificationsUnreadCount: '{count} unread',
    notificationsError: 'Error',
    notificationsWarning: 'Warning',
    notificationsSuccess: 'Success',
    bannerMore: 'Show {count} more',
    resizePanel: 'Resize the panel',
    stepOf: 'Step {step} of {total}: {label}',
    stepDone: ', done',
    stepError: ', needs attention',
    discardTitle: 'Discard your changes?',
    discardText: 'What you entered is lost.',
    discard: 'Discard',
    keepEditing: 'Keep editing',
    imageOf: 'Image {index} of {total}',
    images: 'Images',
    zoomIn: 'Zoom in',
    zoomOut: 'Zoom out',
    imageFailed: 'The image could not be loaded.',
    resizeSheet: 'Resize the sheet',
    sheetDetent: 'Sheet at {percent} percent of the screen',
    filterMatch: '{count} match',
    filterMatches: '{count} matches',
    breadcrumbsMore: 'Show {count} more',
    moreTabs: 'More tabs',
    tabClosed: '{label} closed',
    closeTab: 'Close {label}',
    backToTop: 'Back to top',
    editing: 'Editing {label}',
    sectionSaved: '{label} saved',
    presenceOnline: 'Online',
    presenceAway: 'Away',
    presenceBusy: 'Busy',
    presenceOffline: 'Offline',
    avatarStatus: '{name}, {status}',
    avatarMore: '{count} more',
    avatarOverflow: '+{count}',
    mediaSelected: '{count} selected',
    mediaSelectionCleared: 'Selection cleared',
    levelmeterValue: '{value} dB',
    levelmeterLoud: 'Too loud',
    copyCode: 'Copy code',
    codeBlock: 'Code',
    wrapLines: 'Wrap lines',
    footnotes: 'Footnotes',
    footnoteBack: 'Back to reference {number}',
    calloutNote: 'Note',
    calloutTip: 'Tip',
    calloutImportant: 'Important',
    calloutWarning: 'Warning',
    calloutCaution: 'Caution',
    diffAdded: 'Added',
    diffRemoved: 'Removed',
    diffStats: '{added} added, {removed} removed',
    diffFileAdded: 'New file',
    diffFileDeleted: 'Deleted',
    diffFileRenamed: 'Renamed',
    diffFileCopied: 'Copied',
    diffFileBinary: 'Binary',
    diffBinary: 'Binary file, not shown',
    diffNoChanges: 'No changes',
    diffUntitled: 'Untitled',
    diffNoNewline: 'No newline at end of file',
    diffExpandOne: 'Show 1 unchanged line',
    diffExpand: 'Show {count} unchanged lines',
    diffShownOne: '1 more line shown',
    diffShown: '{count} more lines shown',
    diffOldLine: 'Old line',
    diffNewLine: 'New line',
    diffChange: 'Change',
    diffCode: 'Code',
    diffOldCode: 'Before',
    diffNewCode: 'After',
    logName: 'Log',
    logFatal: 'Fatal',
    logError: 'Error',
    logWarn: 'Warning',
    logInfo: 'Info',
    logDebug: 'Debug',
    logTrace: 'Trace',
    logLevels: 'Levels',
    logSearch: 'Search the log',
    logSearchPlaceholder: 'Search',
    logPrevious: 'Previous match',
    logNext: 'Next match',
    logMatch: '{index} of {count}',
    logMatchesOne: '1 match',
    logMatches: '{count} matches',
    logOnlyMatches: 'Only matching lines',
    logWrap: 'Wrap lines',
    logTimestamps: 'Timestamps',
    logCopyLine: 'Copy line',
    logCopyLines: 'Copy {count} lines',
    logCopiedOne: 'Copied 1 line',
    logCopied: 'Copied {count} lines',
    logSelected: '{count} lines selected',
    logLinesOne: '1 line',
    logLines: '{count} lines',
    logShown: '{shown} of {count} lines',
    logEmpty: 'No lines yet',
    logBlank: 'Blank line',
    logNoneShown: 'No line matches the filters',
    logLatest: 'Latest',
    logFollowing: 'Following new lines',
    terminalKeys: 'Terminal keys',
    terminalUp: 'Arrow up',
    terminalDown: 'Arrow down',
    terminalLeft: 'Arrow left',
    terminalRight: 'Arrow right',
    terminalConnecting: 'Connecting',
    terminalConnected: 'Connected',
    terminalDisconnected: 'Disconnected',
    terminalHeld: '{key} held for the next key',
    terminalReleased: '{key} released',
    send: 'Send',
    stop: 'Stop',
    removeNamed: 'Remove {name}',
    filesAttached: 'Attached {names}',
    fileRemoved: 'Removed {name}',
    resultCount: 'Results: {count}',
    latest: 'Latest',
    unreadCount: '{count} new',
    earlierLoaded: 'Earlier messages loaded',
    messageStopped: 'Stopped',
    messageFailed: 'Not completed',
    feedbackThanks: 'Thanks for the feedback',
    tokensOne: '{count} token',
    tokensOther: '{count} tokens',
    reasoningRunning: 'Thinking',
    reasoningDone: 'Thought for {time}',
    toolStatus: '{name}: {status}',
    toolPending: 'Pending',
    toolRunning: 'Running',
    toolDone: 'Done',
    toolFailed: 'Failed',
    toolCancelled: 'Cancelled',
    usageOf: '{used} of {budget}',
    usageShare: '{percent} used',
    usageNear: 'Near the budget',
    usageOver: 'Over budget',
    planWaiting: 'Waiting for approval',
    planApproved: 'Approved',
    planRejected: 'Rejected',
    questionAnswered: 'Answered: {answer}',
    questionSkipped: 'Skipped',
    questionRequired: 'Choose an answer.',
    });
    const core = runtime.state('core', () => ({
    specs: new Map(),
    elements: new WeakMap(),
    roots: new Map(),
    settings: { locale: null, strings: {} },
    }));
    const MANAGED = 'data-ds-managed';
    const NAME = /^[a-z][\w-]*$/;
    const ATTRIBUTE = /^data-ds-[a-z0-9][a-z0-9-]*$/;
    const noop = () => {};
    function report(error) {
    if (typeof globalThis.reportError === 'function') globalThis.reportError(error);
    else globalThis.console?.error?.(error);
    }
    const isNode = (value) => Boolean(value) && typeof value === 'object' && typeof value.nodeType === 'number';
    const viewOf = (node) => (node.nodeType === 9 ? node : node.ownerDocument)?.defaultView ?? globalThis;
    const isManaged = (element) => {
    const owner = element.closest?.(`[${MANAGED}]`);
    return Boolean(owner) && owner.getAttribute(MANAGED) !== 'off';
    };
    const elementSpecs = () => [...core.specs].filter(([, spec]) => spec.init);
    function watchedAttributes() {
    const names = new Set([MANAGED]);
    for (const spec of core.specs.values()) {
    if (spec.attribute) names.add(spec.attribute);
    for (const name of spec.attributes) names.add(name);
    }
    return [...names];
    }
    function define(name, spec) {
    if (typeof name !== 'string' || !NAME.test(name)) throw new TypeError(`DS.core.define: "${name}" is no behaviour name (lower case, digits, - and _)`);
    if (!spec || typeof spec !== 'object') throw new TypeError(`DS.core.define("${name}"): a spec object`);
    if (core.specs.has(name)) return false;
    const attribute = spec.attribute ?? null;
    if (attribute !== null && (typeof attribute !== 'string' || !ATTRIBUTE.test(attribute))) throw new TypeError(`DS.core.define("${name}"): attribute must be a data-ds-* name`);
    if (typeof spec.init !== 'function' && typeof spec.global !== 'function') throw new TypeError(`DS.core.define("${name}"): init(el, ctx) or global(root)`);
    if (spec.init && !attribute && typeof spec.selector !== 'string') throw new TypeError(`DS.core.define("${name}"): init() needs an attribute or a selector`);
    const match = typeof spec.selector === 'string' ? spec.selector : attribute ? `[${attribute}]` : null;
    if (match && typeof globalThis.document?.createDocumentFragment === 'function') {
    try {
    globalThis.document.createDocumentFragment().querySelector(match);
    } catch {
    throw new TypeError(`DS.core.define("${name}"): "${match}" is no selector`);
    }
    }
    const attributes = Object.freeze([...(spec.attributes ?? [])].map(String));
    const entry = Object.freeze({
    attribute,
    match,
    attributes,
    init: typeof spec.init === 'function' ? spec.init : null,
    global: typeof spec.global === 'function' ? spec.global : null,
    });
    core.specs.set(name, entry);
    for (const state of [...core.roots.values()]) {
    if (entry.global) installGlobal(state, name, entry);
    if (entry.init) scan(state.root, state, [[name, entry]]);
    if (state.observer) connect(state);
    }
    return true;
    }
    function attachOne(element, name, spec, state) {
    let entries = core.elements.get(element);
    if (entries?.has(name)) return;
    const abort = typeof AbortController === 'function' ? new AbortController() : null;
    const ctx = Object.freeze({
    name,
    root: state.root,
    signal: abort?.signal ?? null,
    emit: (type, detail, options) => emit(element, type, detail, options),
    t,
    });
    let result;
    try {
    result = spec.init(element, ctx);
    } catch (error) {
    abort?.abort();
    report(error);
    return;
    }
    const controller = result && typeof result === 'object' ? result : null;
    const cleanup = typeof result === 'function' ? result : typeof controller?.destroy === 'function' ? () => controller.destroy() : null;
    if (!entries) core.elements.set(element, (entries = new Map()));
    entries.set(name, {
    state,
    abort,
    cleanup,
    controller,
    handle: controller ?? Object.freeze({ destroy: () => detachOne(element, name) }),
    });
    state.live.add(element);
    }
    function detachOne(element, name) {
    const entries = core.elements.get(element);
    const entry = entries?.get(name);
    if (!entry) return;
    entries.delete(name);
    if (!entries.size) core.elements.delete(element);
    if (![...(entries.values())].some((other) => other.state === entry.state)) entry.state.live.delete(element);
    entry.abort?.abort();
    try {
    entry.cleanup?.();
    } catch (error) {
    report(error);
    }
    }
    function detachAll(element, state) {
    const entries = core.elements.get(element);
    if (!entries) {
    state?.live.delete(element);
    return;
    }
    for (const [name, entry] of [...entries]) if (!state || entry.state === state) detachOne(element, name);
    }
    function scan(node, state, specs = elementSpecs()) {
    if (!specs.length || typeof node?.querySelectorAll !== 'function') return;
    if (node.nodeType === 1 && isManaged(node)) {
    for (const slot of node.querySelectorAll(`[${MANAGED}="off"]`)) {
    const outer = slot.parentNode;
    if (outer?.nodeType === 1 && isManaged(outer)) scan(slot, state, specs);
    }
    return;
    }
    const selector = specs.map(([, spec]) => spec.match).join(', ');
    const found = node.nodeType === 1 && node.matches(selector) ? [node] : [];
    for (const element of node.querySelectorAll(selector)) found.push(element);
    for (const element of found) {
    if (element !== node && isManaged(element)) continue;
    for (const [name, spec] of specs) if (element.matches(spec.match)) attachOne(element, name, spec, state);
    }
    }
    function reconcile(element, changed, state) {
    if (isManaged(element)) return;
    for (const [name, spec] of elementSpecs()) {
    const names = [spec.attribute, ...spec.attributes].filter((attribute) => attribute && changed.has(attribute));
    if (!names.length) continue;
    const entry = core.elements.get(element)?.get(name);
    const matches = element.matches(spec.match);
    if (!entry) {
    if (matches) attachOne(element, name, spec, state);
    } else if (!matches) detachOne(element, name);
    else if (typeof entry.controller?.update === 'function') {
    try {
    entry.controller.update(names);
    } catch (error) {
    report(error);
    }
    } else {
    detachOne(element, name);
    attachOne(element, name, spec, state);
    }
    }
    }
    function get(el, name) {
    const entries = el ? core.elements.get(el) : undefined;
    if (!entries) return null;
    const entry = name === undefined ? entries.values().next().value : entries.get(name);
    return entry?.handle ?? null;
    }
    function manage(el, name) {
    const spec = core.specs.get(name);
    if (!spec?.init || !isNode(el) || el.nodeType !== 1) return null;
    const state = { root: el.getRootNode?.() ?? el.ownerDocument, live: new Set() };
    const watched = [spec.attribute, ...spec.attributes].filter(Boolean);
    const read = () => watched.map((attribute) => el.getAttribute(attribute));
    if (el.matches(spec.match)) attachOne(el, name, spec, state);
    let seen = read();
    let held = true;
    const own = () => {
    const entry = core.elements.get(el)?.get(name);
    return entry?.state === state ? entry : null;
    };
    return {
    get controller() {
    return held ? core.elements.get(el)?.get(name)?.controller ?? null : null;
    },
    update() {
    if (!held) return;
    const now = read();
    const names = watched.filter((attribute, index) => now[index] !== seen[index]);
    seen = now;
    if (!names.length) return;
    const entry = own();
    const matches = el.matches(spec.match);
    if (!entry) {
    if (matches && !core.elements.get(el)?.has(name)) attachOne(el, name, spec, state);
    } else if (!matches) detachOne(el, name);
    else if (typeof entry.controller?.update === 'function') {
    try {
    entry.controller.update(names);
    } catch (error) {
    report(error);
    }
    } else {
    detachOne(el, name);
    attachOne(el, name, spec, state);
    }
    },
    destroy() {
    if (!held) return;
    held = false;
    if (own()) detachOne(el, name);
    },
    };
    }
    function installGlobal(state, name, spec) {
    if (state.globals.has(name)) return;
    let result;
    try {
    result = spec.global(state.root);
    } catch (error) {
    report(error);
    return;
    }
    state.globals.set(name, typeof result === 'function' ? result : typeof result?.destroy === 'function' ? () => result.destroy() : noop);
    }
    function connect(state) {
    state.observer.observe(state.root, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeOldValue: true,
    attributeFilter: watchedAttributes(),
    });
    }
    function startObserver(state) {
    const Observer = viewOf(state.root).MutationObserver ?? globalThis.MutationObserver;
    if (typeof Observer !== 'function') return;
    state.observer = new Observer((records) => {
    state.pending.push(...records);
    if (state.scheduled) return;
    state.scheduled = true;
    queueMicrotask(() => flush(state));
    });
    connect(state);
    }
    function flush(state) {
    state.scheduled = false;
    if (core.roots.get(state.root) !== state) return;
    const records = state.pending.concat(state.observer?.takeRecords?.() ?? []);
    state.pending = [];
    let removals = false;
    const added = new Set();
    const changed = new Map();
    for (const record of records) {
    if (record.type === 'childList') {
    if (record.removedNodes.length) removals = true;
    for (const node of record.addedNodes) if (node.nodeType === 1) added.add(node);
    } else if (record.type === 'attributes') {
    const element = record.target;
    if (element.getAttribute(record.attributeName) === record.oldValue) continue;
    if (!changed.has(element)) changed.set(element, new Set());
    changed.get(element).add(record.attributeName);
    }
    }
    if (removals) for (const element of [...state.live]) if (!element.isConnected) detachAll(element);
    for (const [element, names] of changed) {
    if (!element.isConnected) {
    detachAll(element);
    continue;
    }
    if (names.has(MANAGED)) added.add(element);
    reconcile(element, names, state);
    }
    for (const node of added) {
    if (!node.isConnected) continue;
    let covered = false;
    for (let parent = node.parentNode; parent && !covered; parent = parent.parentNode) covered = added.has(parent);
    if (!covered) scan(node, state);
    }
    }
    function teardown(state) {
    if (core.roots.get(state.root) !== state) return;
    core.roots.delete(state.root);
    state.observer?.disconnect();
    state.observer = null;
    state.pending = [];
    for (const cleanup of [...state.globals.values()].reverse()) {
    try {
    cleanup();
    } catch (error) {
    report(error);
    }
    }
    state.globals.clear();
    for (const element of [...state.live]) detachAll(element, state);
    }
    const holds = (outer, inner) => outer !== inner && typeof outer.contains === 'function' && outer.contains(inner);
    function init(root = globalThis.document, options = {}) {
    if (!isNode(root) || typeof root.querySelectorAll !== 'function') return { destroy: noop };
    const observe = options.observe ?? true;
    const outer = [...core.roots.values()].find((other) => holds(other.root, root));
    if (outer) {
    scan(root, outer);
    let held = true;
    return {
    destroy() {
    if (!held) return;
    held = false;
    destroy(root);
    },
    };
    }
    let state = core.roots.get(root);
    if (state) state.holders += 1;
    else {
    state = { root, holders: 1, globals: new Map(), live: new Set(), observer: null, pending: [], scheduled: false };
    for (const inner of [...core.roots.values()].filter((other) => holds(root, other.root))) {
    const controlled = [...inner.live];
    inner.live.clear();
    teardown(inner);
    for (const element of controlled) {
    for (const entry of core.elements.get(element)?.values() ?? []) if (entry.state === inner) entry.state = state;
    state.live.add(element);
    }
    }
    core.roots.set(root, state);
    for (const [name, spec] of core.specs) if (spec.global) installGlobal(state, name, spec);
    }
    scan(root, state);
    if (observe && !state.observer) startObserver(state);
    const owner = state;
    let held = true;
    return {
    destroy() {
    if (!held) return;
    held = false;
    if (core.roots.get(owner.root) !== owner) return;
    owner.holders -= 1;
    if (owner.holders <= 0) teardown(owner);
    },
    };
    }
    function destroy(root = globalThis.document) {
    if (!isNode(root)) return;
    for (const state of [...core.roots.values()]) if (state.root === root || holds(root, state.root)) teardown(state);
    for (const state of [...core.roots.values()]) {
    for (const element of [...state.live]) if (element === root || holds(root, element)) detachAll(element, state);
    }
    }
    function emit(el, type, detail, options = {}) {
    const name = String(type).startsWith('ds:') ? String(type) : `ds:${type}`;
    const View = el?.ownerDocument?.defaultView ?? globalThis;
    const Custom = View.CustomEvent ?? globalThis.CustomEvent;
    return el.dispatchEvent(new Custom(name, { bubbles: true, cancelable: Boolean(options?.cancelable), detail: detail ?? null }));
    }
    let frozenStrings = null;
    function config(options) {
    const settings = core.settings;
    const doc = globalThis.document;
    const lang = doc?.documentElement?.lang;
    const localeNow = () => settings.locale ?? (lang || globalThis.navigator?.language || 'en');
    const before = localeNow();
    const changed = [];
    if (options && typeof options === 'object') {
    if ('locale' in options) settings.locale = options.locale ? String(options.locale) : null;
    if (localeNow() !== before) changed.push('locale');
    const texts = Object.entries(options.strings ?? {});
    for (const [key, value] of texts) {
    if (value === null || value === undefined) delete settings.strings[key];
    else settings.strings[key] = typeof value === 'function' ? value : String(value);
    }
    if (texts.length) {
    changed.push('strings');
    settings.version = (settings.version ?? 0) + 1;
    }
    }
    if (!frozenStrings || frozenStrings.of !== settings || frozenStrings.version !== (settings.version ?? 0)) {
    frozenStrings = { of: settings, version: settings.version ?? 0, strings: Object.freeze({ ...DEFAULT_STRINGS, ...settings.strings }) };
    }
    const current = Object.freeze({ locale: localeNow(), strings: frozenStrings.strings });
    if (changed.length && typeof doc?.dispatchEvent === 'function') emit(doc, 'config', { ...current, changed });
    return current;
    }
    function t(key, vars) {
    const own = core.settings.strings;
    const text = Object.hasOwn(own, key) ? own[key] : Object.hasOwn(DEFAULT_STRINGS, key) ? DEFAULT_STRINGS[key] : key;
    const values = vars ?? {};
    const value = typeof text === 'function' ? text(values) : text;
    return String(value).replace(/\{(\w+)\}/g, (match, name) => (Object.hasOwn(values, name) ? String(values[name]) : match));
    }
    const SAFE_SCHEMES = new Set(['http:', 'https:', 'mailto:', 'tel:']);
    function safeHref(value) {
    if (value === null || value === undefined) return null;
    const text = String(value);
    if (!text.trim()) return null;
    let absolute = null;
    try {
    absolute = new URL(text);
    } catch {
    return text;
    }
    return SAFE_SCHEMES.has(absolute.protocol) ? text : null;
    }

    return Object.freeze({
      runtime: runtime,
      define: define,
      get: get,
      manage: manage,
      init: init,
      destroy: destroy,
      emit: emit,
      config: config,
      t: t,
      safeHref: safeHref
    });
  });

  add('motion', ['MOTION_FALLBACKS', 'LOADER_DELAY_MS', 'LOADER_MIN_VISIBLE_MS', 'STAGGER_MAX', 'motionDuration', 'motionEasing', 'travelDuration', 'resyncScroll', 'stagger', 'replay', 'settled', 'enter', 'leave', 'cancel', 'enterAll', 'staggerOnce', 'loaderHoldMs', 'settleLoader', 'refresh', 'guard', 'setLabel', 'busy', 'COUNT_UP_MS', 'countUp', 'originFrom', 'roll', 'crossfade', 'flip', 'sortable', 'transition'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, runtime = DS.core.runtime;
    const MOTION_FALLBACKS = Object.freeze({
    '--ds-dur-micro': 50,
    '--ds-dur-instant': 100,
    '--ds-dur-fast': 150,
    '--ds-dur-base': 200,
    '--ds-dur-medium': 250,
    '--ds-dur-slow': 350,
    '--ds-dur-long': 450,
    '--ds-dur-glide': 600,
    '--ds-delay-loader': 200,
    '--ds-delay-boot': 400,
    '--ds-hold-loader': 450,
    '--ds-hold-success': 600,
    '--ds-hold-status': 900,
    '--ds-delay-tooltip': 500,
    '--ds-delay-tooltip-skip': 300,
    '--ds-delay-tooltip-hide': 100,
    '--ds-delay-submenu': 100,
    '--ds-delay-submenu-close': 300,
    '--ds-delay-long-press': 500,
    '--ds-delay-dismiss': 300,
    '--ds-ease-in-out': 'cubic-bezier(.2, 0, 0, 1)',
    '--ds-ease-out': 'cubic-bezier(.05, .7, .1, 1)',
    '--ds-ease-in': 'cubic-bezier(.3, 0, 1, 1)',
    '--ds-ease-exit': 'cubic-bezier(.3, 0, .8, .15)',
    '--ds-ease-emphasized': 'cubic-bezier(.2, 0, 0, 1)',
    '--ds-ease-spatial': 'cubic-bezier(.05, .7, .1, 1)',
    '--ds-ease-value': 'cubic-bezier(.2, 0, 0, 1)',
    });
    const LOADER_DELAY_MS = (MOTION_FALLBACKS['--ds-delay-loader']);
    const LOADER_MIN_VISIBLE_MS = (MOTION_FALLBACKS['--ds-hold-loader']);
    const DEFAULT_TIMEOUT_MS = 1000;
    const CEILING_MARGIN_MS = 100;
    const TRAVEL_FULL_PX = 480;
    const STAGGER_MAX = 5;
    function activeIn(node) {
    const root = node?.getRootNode?.();
    return (root?.nodeType === 11 ? root.activeElement : null) ?? node?.ownerDocument?.activeElement ?? null;
    }
    function windowOf(element) {
    return element?.ownerDocument?.defaultView ?? globalThis;
    }
    function computed(element) {
    const view = windowOf(element);
    return typeof view.getComputedStyle === 'function' ? view.getComputedStyle(element) : null;
    }
    function themed(element) {
    return Boolean(element?.closest?.('.ds-theme'));
    }
    function tokenSource(element) {
    return element?.closest?.('.ds-theme') ?? element;
    }
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    if (!element || typeof element !== 'object') return;
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element?.[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const NUMBER = '[+-]?(?:\\d+\\.?\\d*|\\.\\d+)(?:e[+-]?\\d+)?';
    const TIME = new RegExp(`^(${NUMBER})(ms|s)$`, 'i');
    const PLAIN = new RegExp(`^${NUMBER}$`, 'i');
    function parseTime(text) {
    const match = TIME.exec(text.trim());
    return match ? Number(match[1]) * (match[2].toLowerCase() === 's' ? 1000 : 1) : null;
    }
    function parseDuration(text) {
    const value = text.trim();
    const time = parseTime(value);
    if (time !== null) return time;
    const product = /^calc\(\s*([^\s*()]+)\s*\*\s*([^\s*()]+)\s*\)$/i.exec(value);
    if (!product) return null;
    const [, left, right] = product;
    if (parseTime(left) !== null && PLAIN.test(right)) return parseTime(left) * Number(right);
    if (PLAIN.test(left) && parseTime(right) !== null) return Number(left) * parseTime(right);
    return null;
    }
    const probed = new Map();
    let probe = (null);
    function probeDuration(element, text) {
    if (probed.has(text)) return probed.get(text);
    const doc = element.ownerDocument;
    const parent = doc?.body ?? doc?.documentElement;
    let ms = null;
    if (parent && typeof doc.createElement === 'function') {
    if (!probe || probe.ownerDocument !== doc) probe = doc.createElement('span');
    probe.hidden = true;
    probe.style.transitionDuration = '';
    probe.style.transitionDuration = text;
    if (probe.style.transitionDuration) {
    parent.append(probe);
    ms = parseTime(computed(probe)?.transitionDuration.split(',')[0] ?? '');
    probe.remove();
    }
    }
    if (probed.size >= 64) probed.clear();
    probed.set(text, ms);
    return ms;
    }
    function motionDuration(element, token) {
    const fallback = typeof MOTION_FALLBACKS[token] === 'number' ? (MOTION_FALLBACKS[token]) : 0;
    if (!themed(element)) return fallback;
    const text = computed(tokenSource(element))?.getPropertyValue(token).trim();
    if (!text) return fallback;
    return parseDuration(text) ?? probeDuration( (element), text) ?? fallback;
    }
    function motionEasing(element, token) {
    const fallback = typeof MOTION_FALLBACKS[token] === 'string' ? (MOTION_FALLBACKS[token]) : 'ease';
    if (!themed(element)) return fallback;
    return computed(tokenSource(element))?.getPropertyValue(token).trim() || fallback;
    }
    function travelDuration(element, px) {
    return travelBetween(motionDuration(element, '--ds-dur-medium'), motionDuration(element, '--ds-dur-slow'), px);
    }
    function travelBetween(medium, slow, px) {
    return medium + (slow - medium) * Math.min(1, Math.abs(Number(px) || 0) / TRAVEL_FULL_PX);
    }
    function splitList(text) {
    const items = [];
    let depth = 0;
    let start = 0;
    for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === '(') depth += 1;
    else if (char === ')') depth -= 1;
    else if (char === ',' && depth === 0) {
    items.push(text.slice(start, index).trim());
    start = index + 1;
    }
    }
    items.push(text.slice(start).trim());
    return items.filter(Boolean);
    }
    function linearPoints(body) {
    const points = [];
    for (const part of body.split(',')) {
    const [value, ...stops] = part.trim().split(/\s+/);
    if (!PLAIN.test(value ?? '') || stops.length > 2) return null;
    const positions = stops.map((stop) => (/%$/.test(stop) && PLAIN.test(stop.slice(0, -1)) ? Number(stop.slice(0, -1)) / 100 : NaN));
    if (positions.some((at) => Number.isNaN(at))) return null;
    if (positions.length) positions.forEach((at) => points.push({ value: Number(value), at }));
    else points.push({ value: Number(value), at: (null) });
    }
    if (points.length < 2) return null;
    points[0].at ??= 0;
    points.at(-1).at ??= 1;
    let highest = 0;
    for (const point of points) {
    if (point.at === null) continue;
    point.at = Math.max(point.at, highest);
    highest = point.at;
    }
    for (let index = 1; index < points.length; index += 1) {
    if (points[index].at !== null) continue;
    let next = index;
    while (points[next].at === null) next += 1;
    const from = (points[index - 1].at);
    const span = (points[next].at) - from;
    for (let step = index; step < next; step += 1) points[step].at = from + (span * (step - index + 1)) / (next - index + 1);
    index = next;
    }
    return (points);
    }
    function bezier(x1, y1, x2, y2) {
    const cx = 3 * x1;
    const bx = 3 * (x2 - x1) - cx;
    const ax = 1 - cx - bx;
    const cy = 3 * y1;
    const by = 3 * (y2 - y1) - cy;
    const ay = 1 - cy - by;
    const x = (s) => ((ax * s + bx) * s + cx) * s;
    const y = (s) => ((ay * s + by) * s + cy) * s;
    const slope = (s) => (3 * ax * s + 2 * bx) * s + cx;
    return ( t) => {
    if (t <= 0) return 0;
    if (t >= 1) return 1;
    let s = t;
    for (let step = 0; step < 8; step += 1) {
    const error = x(s) - t;
    if (Math.abs(error) < 1e-7) return y(s);
    const d = slope(s);
    if (Math.abs(d) < 1e-6) break;
    s -= error / d;
    if (s < 0 || s > 1) break;
    }
    let low = 0;
    let high = 1;
    s = t;
    for (let step = 0; step < 50; step += 1) {
    const value = x(s);
    if (Math.abs(value - t) < 1e-7) break;
    if (value < t) low = s;
    else high = s;
    s = (low + high) / 2;
    }
    return y(s);
    };
    }
    const KEYWORDS = ({
    ease: [0.25, 0.1, 0.25, 1],
    'ease-in': [0.42, 0, 1, 1],
    'ease-out': [0, 0, 0.58, 1],
    'ease-in-out': [0.42, 0, 0.58, 1],
    });
    function curve(css) {
    const text = String(css ?? '').trim().toLowerCase();
    if (Object.hasOwn(KEYWORDS, text)) return bezier(...KEYWORDS[text]);
    const cubic = /^cubic-bezier\(([^)]*)\)$/.exec(text);
    if (cubic) {
    const numbers = cubic[1].split(',').map((part) => Number(part.trim()));
    if (numbers.length === 4 && numbers.every(Number.isFinite)) return bezier(...numbers);
    }
    const linear = /^linear\((.*)\)$/.exec(text);
    const points = linear ? linearPoints(linear[1]) : null;
    if (points) {
    return (t) => {
    const last = points.at(-1);
    if (t >= last.at) return last.value;
    if (t <= points[0].at) return points[0].value;
    let index = 0;
    while (points[index + 1].at <= t) index += 1;
    const from = points[index];
    const to = points[index + 1];
    return from.value + ((to.value - from.value) * (t - from.at)) / (to.at - from.at);
    };
    }
    return (t) => Math.min(1, Math.max(0, t));
    }
    function cubicFor(easing, token) {
    if (token && typeof MOTION_FALLBACKS[token] === 'string') return (MOTION_FALLBACKS[token]);
    const points = linearPoints(easing.slice(easing.indexOf('(') + 1, easing.lastIndexOf(')')));
    const springy = Boolean(points?.some((point) => point.value > 1));
    return (MOTION_FALLBACKS[springy ? '--ds-ease-spatial' : '--ds-ease-emphasized']);
    }
    function animateSafe(element, keyframes, timing, token) {
    try {
    return element.animate(keyframes, timing);
    } catch (error) {
    const easing = String(timing.easing ?? '');
    if ( (error)?.name !== 'TypeError' || !easing.includes('linear(')) throw error;
    return element.animate(keyframes, { ...timing, easing: cubicFor(easing, token) });
    }
    }
    const states = new WeakMap();
    function stateOf(element) {
    let state = states.get(element);
    if (!state) {
    state = { gen: 0, opener: null, handoffs: [], inert: false, marked: false };
    states.set(element, state);
    }
    return state;
    }
    const MOTION_CLASS = /^ds-(enter|leave)(--[\w-]+)?$/;
    const stepOwners = new WeakMap();
    function tidyStyle(element) {
    if (element?.getAttribute?.('style')?.trim() === '') element.removeAttribute('style');
    }
    function dropStep(element) {
    if (!stepOwners.has(element)) return;
    stepOwners.delete(element);
    (element).style.removeProperty('--ds-i');
    tidyStyle(element);
    }
    function clearMotion(element) {
    const names = Array.from(element.classList).filter((name) => MOTION_CLASS.test(name));
    if (names.length) element.classList.remove(...names);
    return names.length > 0;
    }
    function flushStyle(element) {
    void computed(element)?.animationName;
    }
    function cancelHandoffs(element, state = stateOf(element)) {
    for (const animation of state.handoffs) animation.cancel();
    state.handoffs = [];
    }
    function markLeaving(element, state, on) {
    if (on) {
    if (state.marked || element.hasAttribute?.('data-ds-leaving')) return;
    element.setAttribute?.('data-ds-leaving', '');
    state.marked = true;
    } else if (state.marked) {
    element.removeAttribute?.('data-ds-leaving');
    state.marked = false;
    }
    }
    function scrollDriven(animation) {
    return typeof animation.timeline?.axis === 'string';
    }
    function resyncScroll(root = globalThis.document) {
    const doc = root?.nodeType === 9 ? root : root?.ownerDocument;
    const win = doc?.defaultView ?? globalThis;
    if (typeof win.ScrollTimeline !== 'function' || typeof doc?.getAnimations !== 'function' || typeof win.requestAnimationFrame !== 'function') return Promise.resolve(0);
    const related = (node) => Boolean(node) && (root === doc || root.contains(node) || node.contains?.(root));
    return new Promise((resolve) => {
    win.requestAnimationFrame(() => win.requestAnimationFrame(() => {
    let restarted = 0;
    for (const animation of doc.getAnimations()) {
    if (!scrollDriven(animation) || animation.timeline.currentTime !== null || animation.currentTime === null) continue;
    if (!related(animation.timeline.source) && !related(animation.effect?.target)) continue;
    animation.cancel();
    animation.play();
    restarted += 1;
    }
    resolve(restarted);
    }));
    });
    }
    function ownAnimations(element) {
    if (typeof element.getAnimations !== 'function') return [];
    return element.getAnimations().filter((animation) => !animation.effect?.pseudoElement && !scrollDriven(animation));
    }
    function iterationsOf(animation) {
    const timing = animation.effect?.getComputedTiming?.() ?? animation.effect?.getTiming?.() ?? {};
    return timing.iterations ?? 1;
    }
    function holdsMotion(animation) {
    const name = animation.animationName;
    const ours = animation.id === 'ds-handoff' || (typeof name === 'string' && name.startsWith('ds-'));
    if (!ours || iterationsOf(animation) === Infinity) return false;
    if (animation.playState === 'running' || animation.playState === 'paused') return true;
    if (animation.playState !== 'finished') return false;
    const fill = animation.effect?.getComputedTiming?.().fill;
    return fill === 'forwards' || fill === 'both';
    }
    function animationNames(element) {
    const value = computed(element)?.animationName;
    return typeof value === 'string' ? splitList(value).filter((name) => name !== 'none') : undefined;
    }
    const TRACKED = ['opacity', 'translate', 'scale', 'clip-path', 'stroke-dashoffset'];
    const camel = (property) => property.replace(/-([a-z])/g, (match, char) => char.toUpperCase());
    const kebab = (property) => property.replace(/[A-Z]/g, (char) => `-${char.toLowerCase()}`);
    function readStyle(element, properties) {
    const style = computed(element);
    return Object.fromEntries(properties.map((property) => [property, style ? style.getPropertyValue(property) : '']));
    }
    function trackedProperties(animation) {
    const names = new Set();
    for (const keyframe of animation.effect?.getKeyframes?.() ?? []) {
    for (const key of Object.keys(keyframe)) if (TRACKED.includes(kebab(key))) names.add(kebab(key));
    }
    return [...names];
    }
    function numbersOf(property, value) {
    if (!value || value === 'none') return property === 'scale' ? [1, 1, 1] : [0, 0, 0];
    return (value.match(new RegExp(NUMBER, 'gi')) ?? []).map(Number);
    }
    function remainingShare(tracks, now) {
    const ordered = [...tracks].sort((a, b) => Number(b.properties.includes('opacity')) - Number(a.properties.includes('opacity')));
    for (const track of ordered) {
    for (const property of track.properties) {
    const start = numbersOf(property, track.start[property]);
    const end = numbersOf(property, track.end[property]);
    const current = numbersOf(property, now[property]);
    let share = -1;
    start.forEach((value, index) => {
    const span = Math.abs(value - (end[index] ?? 0));
    if (span > 1e-3 && Number.isFinite(current[index])) share = Math.max(share, Math.abs(current[index] - (end[index] ?? 0)) / span);
    });
    if (share >= 0) return Math.min(1, share);
    }
    }
    return 1;
    }
    function handoff(element, classes, leaving, state) {
    const now = readStyle(element, TRACKED);
    for (const animation of ownAnimations(element)) if (holdsMotion(animation)) animation.cancel();
    state.handoffs = [];
    clearMotion(element);
    flushStyle(element);
    element.classList.add(...classes);
    const names = animationNames(element) ?? [];
    const easings = splitList(computed(element)?.animationTimingFunction ?? '');
    const used = new Set();
    const tracks = [];
    for (const animation of ownAnimations(element)) {
    const name = animation.animationName;
    if (typeof name !== 'string' || !names.includes(name) || iterationsOf(animation) === Infinity) continue;
    let index = names.indexOf(name);
    while (used.has(index) && names.indexOf(name, index + 1) >= 0) index = names.indexOf(name, index + 1);
    used.add(index);
    const timing = animation.effect.getComputedTiming();
    const properties = trackedProperties(animation);
    animation.pause();
    animation.currentTime = 0;
    const start = readStyle(element, properties);
    animation.currentTime = Number(timing.endTime) || 0;
    const end = readStyle(element, properties);
    tracks.push({ animation, properties, start, end, easing: easings[index % (easings.length || 1)] || 'linear', duration: Number(timing.duration) || 0 });
    }
    for (const track of tracks) track.animation.cancel();
    const share = remainingShare(tracks, now);
    const floor = motionDuration(element, '--ds-dur-micro');
    const covered = new Set(tracks.flatMap((track) => track.properties));
    const rest = readStyle(element, TRACKED.filter((property) => !covered.has(property)));
    const longest = Math.max(0, ...tracks.map((track) => track.duration)) || motionDuration(element, leaving ? '--ds-dur-fast' : '--ds-dur-medium');
    for (const [property, value] of Object.entries(rest)) {
    if (!differs(property, now[property], value)) continue;
    const opacity = property === 'opacity';
    let easing = leaving ? easings.at(-1) : (easings.length > 1 && !opacity ? easings.at(-1) : '');
    let token;
    if (!easing) {
    token = opacity ? '--ds-ease-in-out' : '--ds-ease-spatial';
    easing = motionEasing(element, token);
    }
    const end = value === 'none' && property === 'clip-path' && now[property].startsWith('inset(') ? 'inset(0px)' : value;
    tracks.push({ properties: [property], end: { [property]: end }, easing, token, duration: longest });
    }
    for (const track of tracks) {
    if (!track.properties.length) continue;
    const from = {};
    const to = {};
    for (const property of track.properties) {
    from[camel(property)] = now[property];
    to[camel(property)] = track.end[property];
    }
    const animation = animateSafe(element, [from, to], {
    id: 'ds-handoff',
    duration: Math.max(floor, track.duration * share),
    delay: 0,
    easing: track.easing,
    fill: leaving ? 'forwards' : 'none',
    }, track.token);
    animation.id = 'ds-handoff';
    state.handoffs.push(animation);
    }
    return names;
    }
    function differs(property, a, b) {
    if (a === b) return false;
    const pad = (values) => (property === 'scale' && values.length === 1 ? [values[0], values[0]] : values);
    const x = pad(numbersOf(property, a));
    const y = pad(numbersOf(property, b));
    const identity = property === 'scale' ? 1 : 0;
    return Array.from({ length: Math.max(x.length, y.length) }, (_, index) => index)
    .some((index) => Math.abs((x[index] ?? identity) - (y[index] ?? identity)) > 1e-3);
    }
    function play(element, classes, leaving, state) {
    return playEach([{ element, classes, state }], leaving)[0];
    }
    function playEach(entries, leaving) {
    const moving = entries.map(({ element }) => ownAnimations(element).some(holdsMotion));
    let dirty = null;
    entries.forEach(({ element, state }, index) => {
    if (moving[index]) return;
    cancelHandoffs(element, state);
    if (clearMotion(element)) dirty ??= element;
    });
    if (dirty) flushStyle(dirty);
    entries.forEach(({ element, classes }, index) => {
    if (!moving[index]) element.classList.add(...classes);
    });
    return entries.map(({ element, classes, state }, index) => (moving[index]
    ? handoff(element, classes, leaving, state)
    : animationNames(element)));
    }
    function releaseFocus(element, focus, state) {
    const active = activeIn(element);
    if (!active || !element.contains?.(active)) return;
    const usable = (node) => Boolean(node) && node.isConnected !== false && !element.contains(node)
    && typeof node.focus === 'function' && !node.closest?.('[inert]');
    const wanted = typeof focus === 'function' ? focus() : focus;
    const target = [wanted, state.opener, element.parentElement?.closest?.('[tabindex="-1"]')].find(usable);
    if (target) target.focus({ preventScroll: true });
    const still = activeIn(element);
    if (still && element.contains(still)) still.blur?.();
    }
    function stagger(elements, options = {}) {
    const from = options.from || 0;
    const max = options.max === undefined ? STAGGER_MAX : options.max;
    let index = 0;
    for (const element of elements) {
    (element).style.setProperty('--ds-i', String(Math.min(from + index, max)));
    index += 1;
    }
    }
    function replay(element, ...classNames) {
    if (!element) return;
    if (classNames.some((name) => element.classList.contains(name))) {
    element.classList.remove(...classNames);
    if (typeof element.getAnimations === 'function') void element.getAnimations();
    else flushStyle(element);
    }
    element.classList.add(...classNames);
    }
    function timeScale(element) {
    const style = computed(element);
    const value = style ? parseFloat(style.getPropertyValue('--ds-time-scale')) : NaN;
    return value > 0 ? value : 1;
    }
    function nextFrame(view) {
    return new Promise((resolve) => {
    const timer = setTimeout(resolve, CEILING_MARGIN_MS);
    if (typeof view.requestAnimationFrame === 'function') {
    view.requestAnimationFrame(() => {
    clearTimeout(timer);
    resolve(undefined);
    });
    }
    });
    }
    function remainingMs(animation) {
    const timing = animation.effect?.getComputedTiming?.() ?? {};
    const end = Number(timing.endTime);
    if (!Number.isFinite(end)) return DEFAULT_TIMEOUT_MS;
    const rate = Math.abs(animation.playbackRate ?? 1) || 1;
    return Math.max(0, end - (Number(animation.currentTime) || 0)) / rate;
    }
    function watched(element, options) {
    const transitionsOnly = options.event === 'transitionend';
    const transitions = transitionsOnly || options.transitions === true;
    const names = options.names;
    return element.getAnimations({ subtree: options.subtree === true }).filter((animation) => {
    if (!animation.effect || animation.effect.pseudoElement || scrollDriven(animation)) return false;
    if (animation.playState === 'idle' || iterationsOf(animation) === Infinity) return false;
    if (typeof animation.transitionProperty === 'string') {
    return transitions && (!options.property || animation.transitionProperty === options.property);
    }
    if (transitionsOnly) return false;
    if (typeof animation.animationName === 'string') return !names || names.includes(animation.animationName);
    return animation.id === 'ds-handoff' || Boolean(names?.includes(animation.id));
    });
    }
    function allFinished(list, timeout) {
    return new Promise((resolve) => {
    let timer = 0;
    let done = false;
    let wasPaused = false;
    const finish = () => {
    done = true;
    clearTimeout(timer);
    resolve(undefined);
    };
    const arm = (first) => {
    if (done) return;
    const open = list.filter((animation) => animation.playState === 'running' || animation.playState === 'paused');
    const paused = timeout === undefined && open.some((animation) => animation.playState === 'paused');
    if (!first && !paused && !wasPaused) {
    finish();
    return;
    }
    wasPaused = paused;
    const wait = first && timeout !== undefined ? timeout : Math.max(0, ...open.map(remainingMs)) + CEILING_MARGIN_MS;
    timer = (setTimeout(() => arm(false), wait));
    };
    arm(true);
    Promise.allSettled(list.map((animation) => animation.finished)).then(finish);
    });
    }
    function settledByEvents(element, options) {
    const transitions = options.event === 'transitionend' || options.transitions === true;
    const types = transitions ? ['transitionend', 'transitioncancel'] : ['animationend', 'animationcancel'];
    const expected = !transitions && options.names?.length ? new Set(options.names) : null;
    const timeout = options.timeout ?? DEFAULT_TIMEOUT_MS * timeScale(element);
    return new Promise((resolve) => {
    let timer = 0;
    const finish = () => {
    clearTimeout(timer);
    for (const type of types) element.removeEventListener(type, done);
    resolve(undefined);
    };
    const done = ( evt) => {
    if (evt.target !== element || evt.pseudoElement) return;
    if (transitions && options.property && evt.propertyName !== options.property) return;
    if (expected && evt.animationName) {
    expected.delete(evt.animationName);
    if (expected.size) return;
    }
    finish();
    };
    for (const type of types) element.addEventListener(type, done);
    timer = (setTimeout(finish, timeout));
    });
    }
    function settled(element, options = {}) {
    if (!element) return Promise.resolve();
    if (typeof element.getAnimations !== 'function') return settledByEvents(element, options);
    const first = watched(element, options);
    if (first.length) return allFinished(first, options.timeout);
    return nextFrame(windowOf(element)).then(() => {
    const later = watched(element, options);
    return later.length ? allFinished(later, options.timeout) : undefined;
    });
    }
    function enter(element, variant = '') {
    if (!element) return Promise.resolve();
    return enterEach([element], variant)[0];
    }
    function enterEach(elements, variant = '') {
    const classes = variant ? ['ds-enter', `ds-enter--${variant}`] : ['ds-enter'];
    const entries = elements.map((element) => {
    const state = stateOf(element);
    const gen = ++state.gen;
    const doc = element.ownerDocument;
    const active = activeIn(element);
    state.opener = active && active !== doc?.body && !element.contains?.(active) ? active : null;
    dropStep(element);
    if ( (element).hidden) (element).hidden = false;
    if ( (element).inert) (element).inert = false;
    state.inert = false;
    markLeaving(element, state, false);
    return { element, classes, state, gen };
    });
    const names = playEach(entries, false);
    return entries.map(({ element, state, gen }, index) => settled(element, { names: names[index] }).then(() => {
    if (state.gen !== gen) return;
    clearMotion(element);
    cancelHandoffs(element, state);
    }));
    }
    function leave(element, variant = '', options = {}) {
    if (!element) return Promise.resolve();
    return leaveEach([element], variant, options)[0];
    }
    function leaveEach(elements, variant = '', options = {}) {
    const then = options.then || 'remove';
    const classes = variant ? ['ds-leave', `ds-leave--${variant}`] : ['ds-leave'];
    const entries = elements.map((element) => {
    const state = stateOf(element);
    const gen = ++state.gen;
    releaseFocus(element, options.focus, state);
    if (!( (element).inert)) {
    (element).inert = true;
    state.inert = true;
    }
    markLeaving(element, state, then === 'remove');
    return { element, classes, state, gen };
    });
    const names = playEach(entries, true);
    return entries.map(({ element, state, gen }, index) => settled(element, { names: names[index] }).then(() => {
    if (state.gen !== gen) return;
    if (then === 'remove') element.remove();
    else if (then === 'hide') {
    (element).hidden = true;
    if (state.inert) (element).inert = false;
    state.inert = false;
    clearMotion(element);
    cancelHandoffs(element, state);
    }
    }));
    }
    function cancel(element) {
    if (!element) return;
    const state = stateOf(element);
    state.gen += 1;
    cancelHandoffs(element, state);
    clearMotion(element);
    dropStep(element);
    if (state.inert) (element).inert = false;
    state.inert = false;
    markLeaving(element, state, false);
    }
    function inView(element, view) {
    const rect = element.getBoundingClientRect?.();
    const width = view.innerWidth;
    const height = view.innerHeight;
    if (!rect || !(width > 0) || !(height > 0)) return true;
    return rect.bottom > 0 && rect.right > 0 && rect.top < height && rect.left < width;
    }
    function enterAll(elements, variant = '', options = {}) {
    const { stagger: staggered = true, max = STAGGER_MAX, visibleOnly = true } = options;
    const items = Array.from(elements ?? []).filter(Boolean);
    if (!items.length) return Promise.resolve();
    const classes = variant ? ['ds-enter', `ds-enter--${variant}`] : ['ds-enter'];
    const batchId = {};
    const moving = items.filter((item) => ownAnimations(item).some(holdsMotion));
    const batch = items.filter((item) => !moving.includes(item));
    const gens = new Map();
    let dirty = null;
    for (const item of batch) {
    const state = stateOf(item);
    gens.set(item, ++state.gen);
    cancelHandoffs(item, state);
    dropStep(item);
    if (item.hidden) item.hidden = false;
    if (item.inert) item.inert = false;
    state.inert = false;
    if (clearMotion(item)) dirty ??= item;
    }
    if (dirty) flushStyle(dirty);
    const shown = visibleOnly && batch.length ? batch.filter((item) => inView(item, windowOf(batch[0]))) : batch;
    shown.forEach((item, index) => {
    if (staggered) {
    item.style.setProperty('--ds-i', String(Math.min(index, max)));
    stepOwners.set(item, batchId);
    }
    item.classList.add(...classes);
    });
    const current = (item) => stateOf(item).gen === gens.get(item);
    const playing = shown.map((item) => settled(item, { names: animationNames(item) }).then(() => {
    if (current(item)) clearMotion(item);
    }));
    const continued = moving.map((item) => enter(item, variant));
    return Promise.all([...playing, ...continued]).then(() => {
    for (const item of shown) if (stepOwners.get(item) === batchId) dropStep(item);
    });
    }
    function staggerOnce(container, options = {}) {
    if (!container || container.hasAttribute('data-ds-staggered')) return Promise.resolve();
    container.setAttribute('data-ds-staggered', '');
    const items = Array.from(container.children).filter((child) => !child.hasAttribute('data-ds-keep'));
    return enterAll(items, options.variant ?? 'swap', { max: options.max ?? STAGGER_MAX, visibleOnly: true });
    }
    function loaderHoldMs(startedAt, now = Date.now(), options = {}) {
    const element = options.element ?? null;
    const delayToken = element?.closest?.('.ds-boot') ? '--ds-delay-boot' : '--ds-delay-loader';
    const delay = options.delay ?? (element ? motionDuration(element, delayToken) : LOADER_DELAY_MS);
    const minVisible = options.minVisible ?? (element ? motionDuration(element, '--ds-hold-loader') : LOADER_MIN_VISIBLE_MS);
    const visible = now - startedAt - delay;
    if (visible <= 0 || visible >= minVisible) return 0;
    return minVisible - visible;
    }
    function settleLoader(startedAt, options, legacy = {}) {
    const numeric = typeof options === 'number';
    const settings = numeric ? legacy : (options !== null && typeof options === 'object' ? options : {});
    const now = numeric ? options : settings.now ?? Date.now();
    const ms = loaderHoldMs(startedAt, now, { element: settings.element, delay: settings.delay, minVisible: settings.minVisible });
    return ms ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
    }
    const refreshes = new WeakMap();
    function abortError(signal) {
    const reason = signal?.reason;
    if (reason instanceof Error) return reason;
    return typeof DOMException === 'function' ? new DOMException('The refresh was aborted', 'AbortError') : Object.assign(new Error('The refresh was aborted'), { name: 'AbortError' });
    }
    function refresh(container, load, options = {}) {
    const signal = options.signal;
    if (signal?.aborted) return Promise.reject(abortError(signal));
    if (!container) return Promise.resolve().then(() => load());
    let run = refreshes.get(container);
    if (!run) {
    const doc = container.ownerDocument;
    run = { count: 0, bar: null, busy: container.getAttribute('aria-busy'), startedAt: Date.now() };
    refreshes.set(container, run);
    own(container, 'classes', 'ds-stale');
    own(container, 'attrs', 'aria-busy');
    container.classList.add('ds-stale');
    container.setAttribute('aria-busy', 'true');
    const parent = container.parentElement;
    if (options.loadbar !== false && parent?.matches?.('.ds-content, .ds-page') && doc) {
    const bar = doc.createElement('span');
    bar.className = 'ds-loadbar';
    bar.setAttribute('aria-hidden', 'true');
    const fill = doc.createElement('span');
    fill.className = 'ds-loadbar__bar';
    bar.append(fill);
    parent.insertBefore(bar, parent.firstChild);
    run.bar = bar;
    }
    }
    run.count += 1;
    const current = run;
    const done = () => {
    current.count -= 1;
    if (current.count > 0) return Promise.resolve();
    return settleLoader(current.startedAt, { element: current.bar ?? container }).then(() => {
    if (current.count > 0 || refreshes.get(container) !== current) return;
    refreshes.delete(container);
    if (current.bar) leave(current.bar);
    container.classList.remove('ds-stale');
    disown(container, 'classes', 'ds-stale');
    disown(container, 'attrs', 'aria-busy');
    if (current.busy === null) container.removeAttribute('aria-busy');
    else container.setAttribute('aria-busy', current.busy);
    });
    };
    const loading = new Promise((resolve, reject) => {
    const onAbort = () => reject(abortError(signal));
    signal?.addEventListener('abort', onAbort, { once: true });
    Promise.resolve().then(() => load()).then(resolve, reject)
    .finally(() => signal?.removeEventListener('abort', onAbort));
    });
    return loading.then(
    (data) => done().then(() => {
    if (signal?.aborted) throw abortError(signal);
    return data;
    }),
    (error) => done().then(() => {
    throw error;
    }),
    );
    }
    const BUSY_ACTION = '.ds-btn:is([aria-busy="true"], .ds-btn--done)';
    const DISABLED_ACTION = `:is(${[
    'a',
    'button',
    'input',
    'summary',
    '[role="button"]',
    '[role="switch"]',
    '[role="checkbox"]',
    '[role="radio"]',
    '[role="tab"]',
    '[role="option"]',
    '[role="menuitem"]',
    '[role="menuitemcheckbox"]',
    '[role="menuitemradio"]',
    ].join(', ')})[aria-disabled="true"]`;
    const GUARDED_EVENTS = ['click', 'auxclick', 'submit'];
    const GUARDED_KEY_EVENTS = ['keydown', 'keyup'];
    const RANGE_HOLD_EVENTS = ['pointerdown', 'keydown', 'focusin'];
    const RANGE_MOVE_EVENTS = ['input', 'change'];
    const NATIVE_ACTION = new Set(['A', 'BUTTON', 'INPUT', 'SUMMARY']);
    const guards = runtime.state('guard', () => new WeakMap());
    function blockedAction(node) {
    const element = node?.nodeType === 1 ? node : node?.parentElement ?? null;
    if (typeof element?.closest !== 'function' || !element.closest('.ds-theme')) return null;
    const action = element.closest(`${BUSY_ACTION}, ${DISABLED_ACTION}`);
    return action && !action.closest('[data-ds-guard="off"]') ? action : null;
    }
    function blockActivation(event) {
    if (event.isTrusted === false) return;
    const source = event.type === 'submit' ? event.submitter : event.target;
    if (!blockedAction(source)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    }
    function blockKeys(event) {
    if (event.isTrusted === false || (event.key !== 'Enter' && event.key !== ' ')) return;
    const action = blockedAction(event.target);
    if (!action || action !== event.target || NATIVE_ACTION.has(action.tagName)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    }
    const heldRanges = new WeakMap();
    function isBlockedRange(node) {
    return node?.tagName === 'INPUT' && node.type === 'range' && blockedAction(node) === node;
    }
    function holdRange(event) {
    if (event.isTrusted !== false && isBlockedRange(event.target)) heldRanges.set(event.target, event.target.value);
    }
    function blockRangeMove(event) {
    if (event.isTrusted === false || !isBlockedRange(event.target)) return;
    if (heldRanges.has(event.target)) event.target.value = heldRanges.get(event.target);
    event.stopImmediatePropagation();
    }
    function holdGuard(root) {
    const installed = guards.get(root);
    if (installed) {
    installed.holders += 1;
    return true;
    }
    const target = root.nodeType === 9 ? root.defaultView ?? root : root;
    if (typeof target?.addEventListener !== 'function') return false;
    for (const type of GUARDED_EVENTS) target.addEventListener(type, blockActivation, true);
    for (const type of GUARDED_KEY_EVENTS) target.addEventListener(type, blockKeys, true);
    for (const type of RANGE_HOLD_EVENTS) target.addEventListener(type, holdRange, true);
    for (const type of RANGE_MOVE_EVENTS) target.addEventListener(type, blockRangeMove, true);
    guards.set(root, { target, holders: 1, activation: blockActivation, keys: blockKeys, rangeHold: holdRange, rangeMove: blockRangeMove });
    return true;
    }
    function releaseGuard(root) {
    const installed = guards.get(root);
    if (!installed) return;
    installed.holders -= 1;
    if (installed.holders > 0) return;
    for (const type of GUARDED_EVENTS) installed.target.removeEventListener(type, installed.activation, true);
    for (const type of GUARDED_KEY_EVENTS) installed.target.removeEventListener(type, installed.keys, true);
    for (const type of RANGE_HOLD_EVENTS) installed.target.removeEventListener(type, installed.rangeHold, true);
    for (const type of RANGE_MOVE_EVENTS) installed.target.removeEventListener(type, installed.rangeMove, true);
    guards.delete(root);
    }
    function guard(root = globalThis.document) {
    let held = Boolean(root) && holdGuard(root);
    return {
    destroy() {
    if (!held) return;
    held = false;
    releaseGuard(root);
    },
    };
    }
    define('guard', {
    global(root) {
    const held = guard(root.nodeType === 9 || root.nodeType === 11 ? root : root.getRootNode?.() ?? root.ownerDocument);
    return () => held.destroy();
    },
    });
    const busyGuarded = new WeakSet();
    function guardFor(element) {
    const root = element?.getRootNode?.();
    const usable = root && (root.nodeType === 9 || root.nodeType === 11) ? root : element?.ownerDocument;
    if (!usable || busyGuarded.has(usable)) return;
    if (holdGuard(usable)) busyGuarded.add(usable);
    }
    const busyRuns = new WeakMap();
    const doneTimers = new WeakMap();
    const pendingLabels = new WeakMap();
    function clearDone(button) {
    if (doneTimers.has(button)) {
    clearTimeout(doneTimers.get(button));
    doneTimers.delete(button);
    disown(button, 'classes', 'ds-btn--done');
    disown(button, 'classes', 'ds-btn--failed');
    }
    button.classList?.remove('ds-btn--done', 'ds-btn--failed');
    applyLabel(button);
    }
    function showEnd(button, name, hold) {
    own(button, 'classes', name);
    button.classList.add(name);
    doneTimers.set(button, setTimeout(() => clearDone(button), motionDuration(button, hold)));
    }
    function restFace(button) {
    const faces = Array.from(button.children ?? []).find((child) => child.classList?.contains('ds-btn__faces'));
    if (!faces) return null;
    return Array.from(faces.children ?? []).find((face) => face.classList?.contains('ds-btn__face')
    && !face.classList.contains('ds-btn__face--busy') && !face.classList.contains('ds-btn__face--done')
    && !face.classList.contains('ds-btn__face--failed')) ?? null;
    }
    function writeLabel(slot, text) {
    const texts = Array.from(slot.childNodes ?? []).filter((node) => node.nodeType === 3 && node.nodeValue.trim());
    if (!texts.length) {
    slot.append(slot.ownerDocument.createTextNode(text));
    return;
    }
    texts[0].nodeValue = text;
    for (const node of texts.slice(1)) node.remove();
    }
    function applyLabel(button) {
    if (!pendingLabels.has(button) || busyRuns.has(button) || doneTimers.has(button)) return;
    const text = (pendingLabels.get(button));
    pendingLabels.delete(button);
    const slot = restFace(button) ?? button;
    const label = Array.from(slot.children ?? []).find((child) => child.classList?.contains('ds-btn__label'));
    writeLabel(label ?? slot, text);
    }
    function setLabel(button, text) {
    if (!button) return;
    pendingLabels.set(button, String(text ?? ''));
    applyLabel(button);
    }
    function busy(button, work, options = {}) {
    const start = () => {
    try {
    return typeof work === 'function' ? (work)() : work;
    } catch (error) {
    return Promise.reject(error);
    }
    };
    if (!button) return Promise.resolve(start());
    const running = busyRuns.get(button);
    if (running) return running;
    guardFor(button);
    const face = Boolean(button.querySelector?.(':scope > .ds-btn__faces > .ds-btn__face--done'));
    const done = face && (options.done ?? true);
    const failedFace = Boolean(button.querySelector?.(':scope > .ds-btn__faces > .ds-btn__face--failed'));
    const failed = failedFace && (options.failed ?? true);
    clearDone(button);
    own(button, 'attrs', 'aria-busy');
    button.setAttribute('aria-busy', 'true');
    const startedAt = Date.now();
    const delay = motionDuration(button, '--ds-delay-loader');
    let promise;
    const finish = () => {
    if (busyRuns.get(button) === promise) busyRuns.delete(button);
    button.removeAttribute('aria-busy');
    disown(button, 'attrs', 'aria-busy');
    };
    promise = Promise.resolve(start()).then(
    (value) => settleLoader(startedAt, { element: button, delay }).then(() => {
    finish();
    if (done) showEnd(button, 'ds-btn--done', '--ds-hold-success');
    else applyLabel(button);
    return value;
    }),
    (error) => {
    if (!failed) {
    finish();
    applyLabel(button);
    throw error;
    }
    return settleLoader(startedAt, { element: button, delay }).then(() => {
    finish();
    showEnd(button, 'ds-btn--failed', '--ds-hold-status');
    throw error;
    });
    },
    );
    busyRuns.set(button, promise);
    return promise;
    }
    const COUNT_UP_MS = (MOTION_FALLBACKS['--ds-dur-glide']);
    const counts = new WeakMap();
    const shownValues = new WeakMap();
    const doneHandle = () => ({ cancel() {}, finished: Promise.resolve() });
    function startClock(element, duration) {
    if (typeof element.animate !== 'function') return null;
    try {
    const clock = animateSafe(element, [], { id: 'ds-count', duration });
    clock.id = 'ds-count';
    return clock;
    } catch {
    return null;
    }
    }
    function steady(element, fromText, toText) {
    const busy = element.getAttribute?.('aria-busy') ?? null;
    for (const name of ['min-width', 'font-variant-numeric']) own(element, 'props', name);
    own(element, 'attrs', 'aria-busy');
    element.setAttribute?.('aria-busy', 'true');
    const style = computed(element);
    const inline = element.style;
    const minWidth = typeof inline?.minWidth === 'string' ? inline.minWidth : null;
    if (style && inline && typeof style.fontVariantNumeric === 'string' && !style.fontVariantNumeric.includes('tabular-nums')) {
    inline.fontVariantNumeric = style.fontVariantNumeric === 'normal' ? 'tabular-nums' : `${style.fontVariantNumeric} tabular-nums`;
    }
    if (style && minWidth !== null && style.display !== 'inline' && typeof element.getBoundingClientRect === 'function') {
    element.textContent = fromText;
    const before = element.getBoundingClientRect().width;
    element.textContent = toText;
    const after = element.getBoundingClientRect().width;
    const box = style.boxSizing === 'border-box' ? 0
    : ['paddingLeft', 'paddingRight', 'borderLeftWidth', 'borderRightWidth'].reduce((sum, key) => sum + (parseFloat(style[key]) || 0), 0);
    inline.minWidth = `${Math.max(before, after) - box}px`;
    }
    return () => {
    if (minWidth !== null) inline.minWidth = minWidth;
    tidyStyle(element);
    if (busy === null) element.removeAttribute?.('aria-busy');
    else element.setAttribute?.('aria-busy', busy);
    for (const name of ['min-width', 'font-variant-numeric']) disown(element, 'props', name);
    disown(element, 'attrs', 'aria-busy');
    };
    }
    function countUp(element, to, options = {}) {
    if (!element) return doneHandle();
    const format = options.format || ((value) => String(Math.round(value)));
    const target = Number(to);
    const running = counts.get(element);
    if (running && running.to === target && options.from === undefined) return running.handle;
    const cached = shownValues.get(element);
    let start = options.from !== undefined ? Number(options.from) : cached ?? 0;
    const text = String(element.textContent ?? '');
    if (!running && options.from === undefined && cached !== undefined && text !== format(cached)) {
    const parsed = Number(text.replace(/[^\d.-]/g, ''));
    start = text.trim() && Number.isFinite(parsed) && format(parsed) === text ? parsed : target;
    }
    const from = Number.isFinite(start) ? start : 0;
    running?.stop(false);
    if (!Number.isFinite(target)) {
    element.textContent = format(target);
    shownValues.delete(element);
    return doneHandle();
    }
    const write = (value) => {
    element.textContent = format(value);
    shownValues.set(element, value);
    };
    if (from === target) {
    if (element.textContent !== format(target)) write(target);
    else shownValues.set(element, target);
    return doneHandle();
    }
    const duration = options.duration ?? motionDuration(element, '--ds-dur-glide');
    const win = 'win' in options ? options.win : windowOf(element);
    if (!(duration > 0) || typeof win?.requestAnimationFrame !== 'function' || element.isConnected === false) {
    write(target);
    return doneHandle();
    }
    const ease = curve(motionEasing(element, '--ds-ease-value'));
    const undo = steady(element, format(from), format(target));
    const clock = startClock(element, duration);
    let began = (null);
    let frame = 0;
    let resolve = () => {};
    const finished = new Promise((done) => { resolve = done; });
    const run = {
    to: target,
    handle: { cancel: () => { if (counts.get(element) === run) run.stop(true); }, finished },
    stop(final) {
    win.cancelAnimationFrame?.(frame);
    clock?.cancel();
    if (counts.get(element) === run) counts.delete(element);
    if (final) write(target);
    undo();
    resolve();
    },
    };
    counts.set(element, run);
    const tick = ( time) => {
    if (counts.get(element) !== run) return;
    let elapsed;
    if (clock) {
    elapsed = clock.playState === 'idle' ? duration : Number(clock.currentTime) || 0;
    } else {
    began ??= time;
    elapsed = time - began;
    }
    const progress = Math.min(1, Math.max(0, elapsed / duration));
    if (progress >= 1 || element.isConnected === false) {
    run.stop(true);
    return;
    }
    write(from + (target - from) * Math.min(1, Math.max(0, ease(progress))));
    frame = win.requestAnimationFrame(tick);
    };
    write(from);
    frame = win.requestAnimationFrame(tick);
    return run.handle;
    }
    const round = (value) => Math.round(value * 100) / 100;
    function edges(rect) {
    return { left: rect.left, top: rect.top, right: rect.right ?? rect.left + rect.width, bottom: rect.bottom ?? rect.top + rect.height };
    }
    const CLIP_BLEED = 'calc(-1 * var(--ds-space-6, 24px))';
    const CLIP_FROM = {
    bottom: `${CLIP_BLEED} ${CLIP_BLEED} 100% ${CLIP_BLEED}`,
    top: `100% ${CLIP_BLEED} ${CLIP_BLEED} ${CLIP_BLEED}`,
    right: `${CLIP_BLEED} 100% ${CLIP_BLEED} ${CLIP_BLEED}`,
    left: `${CLIP_BLEED} ${CLIP_BLEED} ${CLIP_BLEED} 100%`,
    };
    function originFrom(trigger, panel) {
    const box = edges(panel.getBoundingClientRect());
    const anchor = typeof (trigger)?.getBoundingClientRect === 'function'
    ? edges( (trigger).getBoundingClientRect())
    : (() => {
    const point = (trigger);
    return { left: point.x, right: point.x, top: point.y, bottom: point.y };
    })();
    const x = Math.min(box.right - box.left, Math.max(0, (anchor.left + anchor.right) / 2 - box.left));
    const y = Math.min(box.bottom - box.top, Math.max(0, (anchor.top + anchor.bottom) / 2 - box.top));
    const gaps = { bottom: box.top - anchor.bottom, top: anchor.top - box.bottom, right: box.left - anchor.right, left: anchor.left - box.right };
    const side = (['top', 'right', 'left'].reduce((best, name) => (gaps[name] > gaps[best] ? name : best), 'bottom'));
    const origin = `${round(x)}px ${round(y)}px`;
    const radius = computed(panel)?.borderRadius || '0px';
    const style = (panel).style;
    style.setProperty('--ds-motion-origin', origin);
    style.setProperty('--ds-clip-from', `inset(${CLIP_FROM[side]} round ${radius})`);
    return { origin, side };
    }
    function swapSlot(element) {
    element.classList.add('ds-swap');
    const nodes = Array.from(element.childNodes ?? []);
    if (nodes.some((node) => node.nodeType === 3 && node.nodeValue.trim())) {
    const span = element.ownerDocument.createElement('span');
    for (const node of nodes) if (node.nodeType !== 1 || !node.hasAttribute('data-ds-leaving')) span.append(node);
    element.append(span);
    }
    return Array.from(element.children).filter((child) => !child.hasAttribute('data-ds-leaving'));
    }
    function setStyles(element, styles) {
    for (const [name, value] of Object.entries(styles)) element.style.setProperty(name, value);
    }
    const swappedOut = new WeakSet();
    function swap(element, outgoing, incoming, enterHooks, leaveHooks) {
    own(element, 'classes', 'ds-swap');
    const leaving = outgoing.map((child) => {
    child.setAttribute('data-ds-leaving', '');
    child.setAttribute('aria-hidden', 'true');
    swappedOut.add(child);
    setStyles(child, leaveHooks);
    return leave(child);
    });
    if (swappedOut.delete(incoming)) {
    incoming.removeAttribute('data-ds-leaving');
    incoming.removeAttribute('aria-hidden');
    for (const name of ['--ds-leave-keyframes', '--ds-leave-duration', '--ds-leave-easing']) incoming.style.removeProperty(name);
    }
    setStyles(incoming, enterHooks);
    element.append(incoming);
    const entering = enter(incoming);
    const gen = stateOf(incoming).gen;
    return Promise.all([entering, ...leaving]).then(() => {
    disown(element, 'classes', 'ds-swap');
    if (stateOf(incoming).gen !== gen) return;
    for (const name of Object.keys(enterHooks)) incoming.style.removeProperty(name);
    for (const name of ['style', 'class']) if (incoming.getAttribute(name) === '') incoming.removeAttribute(name);
    });
    }
    const rollHolds = new WeakMap();
    function rollNow(element, value) {
    const current = swapSlot(element);
    if (current.length === 1 && current[0].textContent === value) return Promise.resolve();
    const line = element.ownerDocument.createElement('span');
    line.textContent = value;
    return swap(element, current, line,
    { '--ds-enter-keyframes': 'ds-fade-in, ds-roll-in', '--ds-enter-delay': 'calc(var(--ds-dur-fast) / 2)' },
    { '--ds-leave-keyframes': 'ds-fade-out, ds-roll-out' });
    }
    function roll(element, text, options = {}) {
    if (!element) return Promise.resolve();
    const value = String(text ?? '');
    if (!options.hold) {
    const held = rollHolds.get(element);
    if (held) {
    clearTimeout(held.timer);
    held.resolve?.();
    rollHolds.delete(element);
    }
    return rollNow(element, value);
    }
    const state = rollHolds.get(element) ?? { shownAt: -Infinity, text: null, timer: 0, waiting: null, resolve: null };
    rollHolds.set(element, state);
    const hold = motionDuration(element, '--ds-hold-status');
    const run = () => {
    const next = (state.text);
    const resolve = state.resolve;
    state.text = null;
    state.timer = 0;
    state.waiting = null;
    state.resolve = null;
    const shown = swapSlot(element);
    if (!(shown.length === 1 && shown[0].textContent === next)) state.shownAt = Date.now();
    const done = rollNow(element, next);
    done.then(() => resolve?.(), () => resolve?.());
    return done;
    };
    state.text = value;
    if (state.waiting) return state.waiting;
    const left = state.shownAt + hold - Date.now();
    if (left <= 0) return run();
    state.waiting = new Promise((resolve) => { state.resolve = resolve; });
    state.timer = setTimeout(run, left);
    return state.waiting;
    }
    function showsB(element) {
    const swap = element.getAttribute('data-swap');
    if (swap) return swap === 'b';
    return Boolean(element.parentElement?.closest('[aria-pressed="true"]'));
    }
    function iconFace(element, which) {
    const existing = Array.from(element.children).find((child) => child.classList.contains(`ds-icon-swap__${which}`));
    if (existing) return existing;
    const face = element.ownerDocument.createElement('span');
    face.className = `ds-icon-swap__${which}`;
    return face;
    }
    function iconNode(element, next) {
    if (next?.nodeType) return next;
    const template = element.ownerDocument.createElement('template');
    template.innerHTML = String(next ?? '').trim();
    const nodes = Array.from(template.content.childNodes);
    if (nodes.length === 1) return nodes[0];
    const span = element.ownerDocument.createElement('span');
    span.append(...nodes);
    return span;
    }
    function iconSwap(element, next) {
    own(element, 'classes', 'ds-icon-swap');
    own(element, 'attrs', 'data-swap');
    element.classList.add('ds-icon-swap');
    const a = iconFace(element, 'a');
    const b = iconFace(element, 'b');
    const built = !a.parentNode || !b.parentNode;
    if (!a.parentNode) {
    for (const node of Array.from(element.childNodes)) if (node !== b) a.append(node);
    element.prepend(a);
    }
    if (!b.parentNode) element.append(b);
    const shown = showsB(element) ? b : a;
    const target = next === a || next === b ? next : shown === a ? b : a;
    if (next !== a && next !== b) {
    const node = iconNode(element, next);
    const same = target.childNodes.length === 1 && target.firstChild.isEqualNode?.(node);
    if (!same) {
    target.replaceChildren(node);
    flushStyle(target);
    }
    }
    if (built) for (const face of [a, b]) flushStyle(face);
    element.setAttribute('data-swap', target === b ? 'b' : 'a');
    const release = () => {
    disown(element, 'classes', 'ds-icon-swap');
    disown(element, 'attrs', 'data-swap');
    };
    return settled(element, { transitions: true, subtree: true }).then(release, (error) => {
    release();
    throw error;
    });
    }
    function crossfade(element, next, options = {}) {
    const kind = options.kind ?? 'text';
    if (kind !== 'text' && kind !== 'icon') throw new TypeError(`crossfade(): unknown kind "${kind}"`);
    if (!element) return Promise.resolve();
    if (kind === 'icon') return iconSwap(element, next);
    const current = swapSlot(element);
    let incoming = (next);
    if (incoming?.nodeType !== 1) {
    const span = element.ownerDocument.createElement('span');
    if (incoming?.nodeType) span.append(incoming);
    else span.textContent = String(incoming ?? '');
    incoming = span;
    }
    return swap(element, current.filter((child) => child !== incoming), incoming,
    { '--ds-enter-keyframes': 'ds-fade-in', '--ds-enter-duration': 'var(--ds-dur-fast)', '--ds-enter-easing': 'var(--ds-ease-in-out)' },
    { '--ds-leave-duration': 'var(--ds-dur-instant)' });
    }
    const flips = new WeakMap();
    const glides = new WeakMap();
    const raisedItems = new WeakMap();
    function flipState(container) {
    let state = flips.get(container);
    if (!state) {
    state = { pointer: '', over: false, guarded: false, items: undefined, queued: null, timer: 0, waiters: [], lifted: 0, position: null, minHeight: null, align: null, sizeGlide: null, raised: 0, isolation: null, backed: false };
    flips.set(container, state);
    }
    return state;
    }
    function flipItems(container, selector) {
    const list = selector ? Array.from(container.querySelectorAll(selector)) : Array.from(container.children ?? []);
    return list.filter((item) => !item.hasAttribute('data-ds-keep') && !item.hasAttribute('data-ds-leaving'));
    }
    function isFlipItem(container, node, selector) {
    if (node?.nodeType !== 1 || node.hasAttribute('data-ds-keep')) return false;
    return selector ? Boolean(node.matches?.(selector)) && container.contains(node) : node.parentElement === container;
    }
    function offscreen(rect, view) {
    const width = view.innerWidth;
    const height = view.innerHeight;
    if (!(width > 0) || !(height > 0)) return false;
    const box = edges(rect);
    return box.bottom <= 0 || box.right <= 0 || box.top >= height || box.left >= width;
    }
    function liftable(node, display) {
    if (node.tagName === 'TR') return false;
    return !(display ?? computed(node)?.display ?? '').startsWith('table');
    }
    function lift(container, node, rect, frame, state, box = liftBox(container)) {
    if (!state.lifted) {
    state.position = null;
    if (box.position === 'static') {
    state.position = container.style.position;
    own(container, 'props', 'position');
    container.style.position = 'relative';
    }
    }
    state.lifted += 1;
    const at = edges(rect);
    const left = at.left - frame.left - box.borderLeft + box.scrollLeft;
    const top = at.top - frame.top - box.borderTop + box.scrollTop;
    setStyles(node, {
    position: 'absolute',
    left: `${round(left)}px`,
    top: `${round(top)}px`,
    width: `${round(at.right - at.left)}px`,
    height: `${round(at.bottom - at.top)}px`,
    'min-width': '0',
    'max-width': 'none',
    'min-height': '0',
    'max-height': 'none',
    margin: '0',
    'box-sizing': 'border-box',
    });
    }
    function liftBox(container) {
    const style = computed(container);
    return {
    position: style?.position ?? '',
    borderLeft: parseFloat(style?.borderLeftWidth) || 0,
    borderTop: parseFloat(style?.borderTopWidth) || 0,
    scrollLeft: container.scrollLeft || 0,
    scrollTop: container.scrollTop || 0,
    };
    }
    function lazyLiftBox(container) {
    let box = null;
    return () => (box ??= liftBox(container));
    }
    function paints(colour) {
    if (!colour || colour === 'transparent') return false;
    const inner = /\(([^)]*)\)/.exec(colour)?.[1] ?? '';
    const slash = inner.split('/');
    if (slash.length > 1) return parseFloat(slash[1]) > 0;
    const parts = inner.split(',');
    return parts.length === 4 ? parseFloat(parts[3]) > 0 : true;
    }
    function backdropOf(element) {
    for (let node = element; node && node.nodeType === 1; node = node.parentElement) {
    const colour = computed(node)?.backgroundColor;
    if (paints(colour)) return colour;
    }
    return '';
    }
    function backContainer(container, state, known) {
    if (state.backed) return;
    state.backed = true;
    own(container, 'attrs', 'data-ds-glide');
    own(container, 'props', '--ds-flip-backdrop');
    const backdrop = known ?? backdropOf(container);
    if (backdrop) container.style.setProperty('--ds-flip-backdrop', backdrop);
    container.setAttribute('data-ds-glide', '');
    }
    function unbackContainer(container, state) {
    if (!state.backed) return;
    state.backed = false;
    container.removeAttribute('data-ds-glide');
    container.style.removeProperty('--ds-flip-backdrop');
    tidyStyle(container);
    disown(container, 'attrs', 'data-ds-glide');
    disown(container, 'props', '--ds-flip-backdrop');
    }
    function raise(container, state, item, rank, layers, known = {}) {
    if (!raisedItems.has(item)) {
    raisedItems.set(item, { position: item.style.position, zIndex: item.style.zIndex, container });
    for (const name of ['position', 'z-index']) own(item, 'props', name);
    if ((known.position ?? computed(item)?.position) === 'static') item.style.position = 'relative';
    if (!state.raised) {
    state.isolation = container.style.isolation ?? '';
    own(container, 'props', 'isolation');
    container.style.isolation = 'isolate';
    backContainer(container, state, known.backdrop);
    }
    state.raised += 1;
    }
    item.style.zIndex = layers ? String(Math.max(layers.base + 1, Math.min(layers.base + 1 + rank, layers.sticky - 1))) : 'calc(var(--ds-z-raised) + 1)';
    }
    function glideLayers(container) {
    const style = computed(container);
    const read = (name) => parseInt(style?.getPropertyValue(name) ?? '', 10);
    const raised = read('--ds-z-raised');
    if (!Number.isFinite(raised)) return null;
    const pinned = read('--ds-z-pinned');
    const sticky = read('--ds-z-sticky');
    const base = Math.max(raised, Number.isFinite(pinned) ? pinned : raised + 1);
    return { base, sticky: Number.isFinite(sticky) ? sticky : Infinity };
    }
    function lower(item) {
    const saved = raisedItems.get(item);
    if (!saved) return;
    raisedItems.delete(item);
    item.style.position = saved.position;
    item.style.zIndex = saved.zIndex;
    tidyStyle(item);
    for (const name of ['position', 'z-index']) disown(item, 'props', name);
    const state = flipState(saved.container);
    state.raised = Math.max(0, state.raised - 1);
    if (!state.raised && state.isolation !== null) {
    saved.container.style.isolation = state.isolation;
    tidyStyle(saved.container);
    state.isolation = null;
    disown(saved.container, 'props', 'isolation');
    }
    if (!state.raised) unbackContainer(saved.container, state);
    }
    function heightOf(element) {
    const box = edges(element.getBoundingClientRect());
    return box.bottom - box.top;
    }
    function minHeightFor(container, height) {
    const style = computed(container);
    if (!style || style.boxSizing === 'border-box') return height;
    return height - ['paddingTop', 'paddingBottom', 'borderTopWidth', 'borderBottomWidth'].reduce((sum, key) => sum + (parseFloat(style[key]) || 0), 0);
    }
    function holdSize(container, state, height) {
    if (state.minHeight !== null) return;
    state.minHeight = container.style.minHeight ?? '';
    own(container, 'props', 'min-height');
    const style = computed(container);
    const display = style?.display ?? '';
    const alignContent = style?.alignContent ?? '';
    container.style.minHeight = `${round(Math.max(0, minHeightFor(container, height)))}px`;
    if (state.align === null && /grid/.test(display) && /^(normal|stretch)$/.test(alignContent)) {
    state.align = container.style.alignContent ?? '';
    own(container, 'props', 'align-content');
    container.style.alignContent = 'start';
    }
    }
    function releaseAlign(container, state) {
    if (state.align === null || state.minHeight !== null || state.sizeGlide) return;
    container.style.alignContent = state.align;
    tidyStyle(container);
    state.align = null;
    disown(container, 'props', 'align-content');
    }
    function glideSize(container, state, from) {
    const to = heightOf(container);
    if (from - to < 0.5 || typeof container.animate !== 'function') {
    releaseAlign(container, state);
    return Promise.resolve();
    }
    const animation = animateSafe(container, [
    { minHeight: `${round(minHeightFor(container, from))}px` },
    { minHeight: `${round(minHeightFor(container, to))}px` },
    ], {
    id: 'ds-flip-size',
    duration: travelDuration(container, from - to),
    easing: motionEasing(container, '--ds-ease-out'),
    }, '--ds-ease-out');
    animation.id = 'ds-flip-size';
    state.sizeGlide = animation;
    return animation.finished.then(() => {
    if (state.sizeGlide === animation) state.sizeGlide = null;
    releaseAlign(container, state);
    }, () => {});
    }
    function landed(container, state, glide = true) {
    state.lifted = Math.max(0, state.lifted - 1);
    if (state.lifted) return Promise.resolve();
    if (state.position !== null) {
    container.style.position = state.position;
    tidyStyle(container);
    state.position = null;
    disown(container, 'props', 'position');
    }
    if (state.minHeight === null) return Promise.resolve();
    const from = heightOf(container);
    container.style.minHeight = state.minHeight;
    tidyStyle(container);
    state.minHeight = null;
    disown(container, 'props', 'min-height');
    if (glide) return glideSize(container, state, from);
    releaseAlign(container, state);
    return Promise.resolve();
    }
    function measure(container, selector) {
    const before = new Map();
    for (const item of flipItems(container, selector)) before.set(item, item.getBoundingClientRect());
    const displays = new Map();
    for (const item of before.keys()) if (item.tagName !== 'TR') displays.set(item, computed(item)?.display ?? '');
    const childrenOf = new Set(Array.from(before.keys()).filter((item) => item.parentElement === container));
    const nextOf = new Map(Array.from(container.childNodes, (node) => [node, node.nextSibling]));
    const active = activeIn(container);
    const owner = active && container.contains?.(active) ? Array.from(before.keys()).find((item) => item.contains(active)) ?? null : null;
    return { before, displays, childrenOf, nextOf, frame: edges(container.getBoundingClientRect()), focus: owner ? { active, owner } : null };
    }
    const FOCUSABLE = 'a[href], area[href], button, input:not([type="hidden"]), select, textarea, summary, iframe, audio[controls], video[controls], [contenteditable]:not([contenteditable="false"]), [tabindex]';
    function focusable(element) {
    if (!element || typeof element.focus !== 'function' || element.isConnected === false) return false;
    if (element.disabled || element.closest?.('[inert], [data-ds-leaving], [hidden]')) return false;
    if (typeof element.getClientRects === 'function' && !element.getClientRects().length) return false;
    return true;
    }
    function itemGone(item, removed) {
    return item.isConnected === false || removed.some((entry) => entry.node === item) || Boolean(item.hasAttribute?.('data-ds-leaving'));
    }
    const TRANSIENT_CLASS = /^ds-(pressed|btn--done|enter|leave)(--|$)/;
    function lastingClasses(element) {
    const value = typeof element?.getAttribute === 'function' ? element.getAttribute('class') ?? '' : '';
    return value.split(/\s+/).filter((name) => name && !TRANSIENT_CLASS.test(name)).sort().join(' ');
    }
    function focusablesIn(item) {
    const own = item.matches?.(FOCUSABLE) ? [item] : [];
    return [...own, ...Array.from(item.querySelectorAll?.(FOCUSABLE) ?? [])].filter(focusable);
    }
    function twinIn(item, old) {
    const classes = lastingClasses(old);
    if (!classes) return null;
    return focusablesIn(item).find((element) => lastingClasses(element) === classes) ?? null;
    }
    function firstIn(item) {
    return focusablesIn(item)[0] ?? null;
    }
    function refocusAfter(container, focus, order, removed, refocus) {
    if (!focus || refocus === false) return;
    const { active, owner } = focus;
    if (!itemGone(owner, removed)) return;
    const now = activeIn(container);
    const doc = container.ownerDocument;
    const lost = !now || now === doc?.body || now === doc?.documentElement || owner.contains(now) || now === active && active.isConnected === false;
    if (!lost) return;
    const index = order.indexOf(owner);
    const alive = (item) => !itemGone(item, removed) && !item.hasAttribute?.('data-ds-keep');
    const bearing = (items) => items.filter((item) => alive(item) && firstIn(item));
    const later = bearing(order.slice(index + 1));
    const earlier = bearing(order.slice(0, index).reverse());
    let target = null;
    for (let step = 0; !target && step < Math.max(later.length, earlier.length); step += 1) {
    target = (later[step] && twinIn(later[step], active)) || (earlier[step] && twinIn(earlier[step], active)) || null;
    }
    const nearest = later[0] ?? earlier[0];
    if (!target && nearest) target = firstIn(nearest);
    if (!target && refocus !== true && refocus !== undefined) {
    const fallback = typeof refocus === 'function' ? refocus() : refocus;
    if (focusable(fallback)) target = fallback;
    }
    target?.focus({ preventScroll: true });
    }
    function runFlip(container, mutate, options, state, first) {
    const selector = options.items;
    const max = options.max ?? 50;
    const view = windowOf(container);
    const { before, displays, childrenOf, nextOf, frame, focus } = first ?? measure(container, selector);
    const box = lazyLiftBox(container);
    const firstHeight = frame.bottom - frame.top;
    for (const item of before.keys()) {
    glides.get(item)?.cancel();
    glides.delete(item);
    lower(item);
    }
    const sizing = state.sizeGlide;
    sizing?.cancel();
    state.sizeGlide = null;
    const inserted = [];
    const removed = [];
    mutate({
    holdOrder: false,
    ...(typeof options.onKeep === 'function' ? { onKeep: options.onKeep } : {}),
    onInsert(node) {
    if (isFlipItem(container, node, selector)) inserted.push(node);
    },
    onRemove(node) {
    const rect = before.get(node);
    if (!rect || !liftable(node, displays?.get(node))) return undefined;
    holdSize(container, state, firstHeight);
    lift(container, node, rect, frame, state, box());
    return new Promise((resolve) => removed.push({ node, resolve }));
    },
    });
    const back = Array.from(before.keys()).filter((item) => !item.parentNode && childrenOf.has(item));
    for (const item of back) item.setAttribute('data-ds-moving', '');
    for (const item of [...back].reverse()) {
    let next = nextOf.get(item) ?? null;
    while (next && next.parentNode !== container) next = nextOf.get(next) ?? null;
    container.insertBefore(item, next);
    }
    if (back.length) void computed(back[0])?.opacity;
    for (const item of back) item.removeAttribute('data-ds-moving');
    for (const item of back) {
    const rect = before.get(item);
    if (!liftable(item, displays?.get(item))) {
    item.remove();
    continue;
    }
    holdSize(container, state, firstHeight);
    lift(container, item, rect, frame, state, box());
    removed.push({ node: item, resolve() {} });
    }
    refocusAfter(container, focus, Array.from(before.keys()), [...removed, ...back.filter((item) => !item.parentNode).map((node) => ({ node }))], options.refocus);
    for (const item of flipItems(container, selector)) {
    if (before.has(item) || inserted.includes(item) || removed.some((entry) => entry.node === item)) continue;
    if (item.getAnimations?.().some((animation) => animation.playState !== 'finished' && !scrollDriven(animation))) continue;
    inserted.push(item);
    }
    const moves = [];
    for (const item of flipItems(container, selector)) {
    const first = before.get(item);
    if (!first) continue;
    const last = item.getBoundingClientRect();
    const dx = first.left - last.left;
    const dy = first.top - last.top;
    if (Math.abs(dx) < 0.5 && Math.abs(dy) < 0.5) continue;
    if (offscreen(first, view) && offscreen(last, view)) continue;
    moves.push({ item, dx, dy });
    }
    const gone = (entry, glide = true) => {
    entry.node.remove();
    entry.resolve();
    return landed(container, state, glide);
    };
    if (moves.length > max) {
    removed.forEach((entry) => gone(entry, false));
    return Promise.resolve();
    }
    const resizing = sizing && state.minHeight === null ? glideSize(container, state, firstHeight) : Promise.resolve();
    const easing = motionEasing(container, '--ds-ease-spatial');
    const layers = moves.length ? glideLayers(container) : null;
    const medium = moves.length ? motionDuration(container, '--ds-dur-medium') : 0;
    const slow = moves.length ? motionDuration(container, '--ds-dur-slow') : 0;
    const ranked = moves.map((move) => ({ move, distance: Math.hypot(move.dx, move.dy) }))
    .sort((a, b) => (a.distance - b.distance) || (a.move.dy - b.move.dy));
    const positions = ranked.map(({ move }) => computed(move.item)?.position);
    const backdrop = ranked.length && !state.raised && !state.backed ? backdropOf(container) : undefined;
    ranked.forEach(({ move }, rank) => raise(container, state, move.item, rank, layers, { position: positions[rank], backdrop }));
    const running = moves.map(({ item, dx, dy }) => {
    const animation = animateSafe(item, [{ translate: `${round(dx)}px ${round(dy)}px` }, { translate: '0px 0px' }], {
    id: 'ds-flip',
    duration: travelBetween(medium, slow, Math.hypot(dx, dy)),
    easing,
    composite: 'add',
    }, '--ds-ease-spatial');
    animation.id = 'ds-flip';
    glides.set(item, animation);
    const ended = () => {
    if (glides.get(item) === animation) glides.delete(item);
    if (!glides.has(item)) lower(item);
    };
    return animation.finished.then(ended, (error) => {
    ended();
    throw error;
    });
    });
    const entering = enterEach(inserted, 'fade');
    const leaving = leaveEach(removed.map((entry) => entry.node)).map((done, index) => done.then(() => gone(removed[index])));
    return Promise.allSettled([...running, ...entering, ...leaving, resizing]).then(() => {});
    }
    function held(container, state) {
    if (state.over && (state.pointer === 'mouse' || state.pointer === 'pen')) return true;
    const active = activeIn(container);
    if (!active || active === container || !container.contains(active)) return false;
    return flipItems(container, state.items).some((item) => item.contains(active));
    }
    function flush(container, mutate, options, state, first) {
    const steps = (state.queued?.steps ?? []).map((step) => step.mutate);
    const waiters = state.waiters;
    clearTimeout(state.timer);
    state.timer = 0;
    state.queued = null;
    state.waiters = [];
    if (mutate && !steps.includes(mutate)) steps.push(mutate);
    const all = steps.length === 1 ? steps[0] : ( hooks) => {
    for (const step of steps) step(hooks);
    };
    const done = runFlip(container, all, options, state, first);
    for (const resolve of waiters) resolve(done);
    return done;
    }
    function release(container, state, force = false) {
    if (!state.queued || (!force && held(container, state))) return;
    flush(container, null, state.queued.options, state);
    }
    function sameItems(a, b) {
    return a.length === b.length && a.every((item, index) => item === b[index]);
    }
    function watchLive(container, state) {
    if (state.guarded) return;
    state.guarded = true;
    try {
    if (container.matches(':hover') && windowOf(container).matchMedia('(hover: hover)').matches) {
    state.over = true;
    state.pointer = 'mouse';
    }
    } catch {
    }
    container.addEventListener('pointerover', ( event) => {
    state.over = true;
    state.pointer = event.pointerType || 'mouse';
    });
    container.addEventListener('pointerleave', () => {
    state.over = false;
    release(container, state);
    });
    container.addEventListener('focusout', ( event) => {
    if (event.relatedTarget && container.contains(event.relatedTarget)) return;
    setTimeout(() => release(container, state), 0);
    });
    }
    function flip(container, mutate, options = {}) {
    if (!container || typeof mutate !== 'function') return Promise.resolve();
    const state = flipState(container);
    if (options.live) {
    state.items = options.items;
    watchLive(container, state);
    if (held(container, state)) {
    const queued = state.queued ?? (state.queued = { steps: [], options });
    queued.options = options;
    if (mutate.length === 0) {
    queued.steps.push({ mutate, aware: false });
    } else {
    const first = measure(container, state.items);
    mutate({ holdOrder: true, ...(typeof options.onKeep === 'function' ? { onKeep: options.onKeep } : {}), onInsert() {}, onRemove() { return undefined; } });
    queued.steps = queued.steps.filter((step) => !step.aware);
    if (!sameItems(Array.from(first.before.keys()), flipItems(container, state.items))) {
    return flush(container, null, options, state, first);
    }
    queued.steps.push({ mutate, aware: true });
    }
    if (!state.timer) state.timer = setTimeout(() => release(container, state, true), options.hold ?? 2000);
    return new Promise((resolve) => state.waiters.push(resolve));
    }
    }
    return flush(container, mutate, options, state);
    }
    const sortables = new WeakMap();
    const sortRegions = new WeakMap();
    function speakPosition(list, text) {
    const host = list.closest?.('dialog, .ds-dialog, .ds-sheet') ?? list.closest?.('.ds-theme') ?? list.ownerDocument?.body;
    if (!host || !text) return;
    let region = sortRegions.get(host);
    if (!region || !region.isConnected) {
    region = list.ownerDocument.createElement('div');
    region.className = 'ds-sr';
    region.setAttribute('aria-live', 'polite');
    region.setAttribute('data-ds-keep', '');
    host.append(region);
    sortRegions.set(host, region);
    }
    region.textContent = '';
    const target = region;
    setTimeout(() => { target.textContent = text; }, 0);
    }
    function sortable(list, options = {}) {
    if (!list?.ownerDocument) return { destroy() {} };
    const existing = sortables.get(list);
    if (existing) return existing.handle;
    const handleSelector = options.handle ?? '.ds-grip';
    const selector = options.items;
    const items = () => flipItems(list, selector);
    const others = selector ? `:is(${selector}):not([data-ds-dragging])` : ':scope > :not([data-ds-dragging])';
    const itemOf = (node) => items().find((item) => item.contains(node)) ?? null;
    const say = (item) => {
    const order = items();
    const text = (options.announce ?? ((index, count) => `Moved to position ${index} of ${count}`))(order.indexOf(item) + 1, order.length);
    speakPosition(list, text);
    };
    const changed = (before) => {
    const after = items();
    if (!sameItems(before, after)) options.onChange?.(after, before);
    };
    const place = (item, reference) => {
    const parent = reference?.parentNode ?? item.parentNode;
    const focused = activeIn(item);
    const keep = focused && item.contains(focused) ? focused : null;
    if (typeof parent.moveBefore === 'function') {
    try {
    parent.moveBefore(item, reference);
    } catch {
    parent.insertBefore(item, reference);
    }
    } else parent.insertBefore(item, reference);
    if (keep && activeIn(item) !== keep) keep.focus({ preventScroll: true });
    };
    const onKey = ( event) => {
    const handle = event.target?.closest?.(handleSelector);
    if (!handle || !list.contains(handle) || event.altKey || event.ctrlKey || event.metaKey) return;
    const item = itemOf(handle);
    if (!item) return;
    const order = items();
    const index = order.indexOf(item);
    const to = { ArrowUp: index - 1, ArrowDown: index + 1, Home: 0, End: order.length - 1 }[event.key];
    if (to === undefined) return;
    event.preventDefault();
    if (to < 0 || to >= order.length || to === index) return;
    const before = order.slice();
    flip(list, () => place(item, to > index ? order[to].nextSibling : order[to]), { items: selector });
    changed(before);
    say(item);
    };
    let drag = null;
    const ending = new Map();
    const finish = (item) => {
    const end = ending.get(item);
    if (!end) return;
    ending.delete(item);
    clearTimeout(end.timer);
    item.removeAttribute('data-ds-dragging');
    item.style.zIndex = end.z;
    tidyStyle(item);
    disown(item, 'attrs', 'data-ds-dragging');
    for (const name of ['translate', 'z-index']) disown(item, 'props', name);
    };
    const layoutTop = (item) => item.offsetTop ?? 0;
    const middle = (item) => layoutTop(item) + (item.offsetHeight ?? 0) / 2;
    const onDown = ( event) => {
    if (drag || event.button !== 0) return;
    const handle = event.target?.closest?.(handleSelector);
    if (!handle || !list.contains(handle)) return;
    const item = itemOf(handle);
    if (!item) return;
    event.preventDefault();
    handle.setPointerCapture?.(event.pointerId);
    for (const animation of item.getAnimations?.() ?? []) if (animation.id === 'ds-sort') animation.cancel();
    finish(item);
    drag = { item, handle, pointer: event.pointerId, startY: event.clientY, dy: 0, before: items(), z: item.style.zIndex };
    own(item, 'attrs', 'data-ds-dragging');
    for (const name of ['translate', 'z-index']) own(item, 'props', name);
    item.setAttribute('data-ds-dragging', '');
    const sticky = parseInt(computed(list)?.getPropertyValue('--ds-z-sticky') ?? '', 10);
    item.style.zIndex = Number.isFinite(sticky) ? String(sticky - 1) : 'calc(var(--ds-z-sticky) - 1)';
    };
    const onMove = ( event) => {
    if (!drag || event.pointerId !== drag.pointer) return;
    const { item } = drag;
    drag.dy = event.clientY - drag.startY;
    item.style.translate = `0px ${round(drag.dy)}px`;
    const order = items();
    const index = order.indexOf(item);
    const top = layoutTop(item) + drag.dy;
    const bottom = top + (item.offsetHeight ?? 0);
    let target = index;
    while (target > 0 && top < middle(order[target - 1])) target -= 1;
    while (target < order.length - 1 && bottom > middle(order[target + 1])) target += 1;
    if (target === index) return;
    const from = layoutTop(item);
    flip(list, () => place(item, target > index ? order[target].nextSibling : order[target]), { items: others });
    const shift = layoutTop(item) - from;
    drag.startY += shift;
    drag.dy -= shift;
    item.style.translate = `0px ${round(drag.dy)}px`;
    say(item);
    };
    const onUp = ( event) => {
    if (!drag || event.pointerId !== drag.pointer) return;
    const { item, handle, dy, before, z } = drag;
    drag = null;
    handle.releasePointerCapture?.(event.pointerId);
    item.style.removeProperty('translate');
    if (Math.abs(dy) >= 0.5 && typeof item.animate === 'function') {
    const glide = animateSafe(item, [{ translate: `0px ${round(dy)}px` }, { translate: '0px 0px' }], {
    id: 'ds-sort',
    duration: travelDuration(list, dy),
    easing: motionEasing(list, '--ds-ease-spatial'),
    }, '--ds-ease-spatial');
    glide.id = 'ds-sort';
    }
    item.setAttribute('data-ds-dragging', 'end');
    const timer = setTimeout(() => finish(item), Math.max(motionDuration(item, '--ds-dur-instant'), motionDuration(list, '--ds-dur-medium')));
    ending.set(item, { timer, z });
    changed(before);
    };
    list.addEventListener('keydown', onKey);
    list.addEventListener('pointerdown', onDown);
    list.addEventListener('pointermove', onMove);
    list.addEventListener('pointerup', onUp);
    list.addEventListener('pointercancel', onUp);
    const handle = {
    destroy() {
    if (sortables.get(list)?.handle !== handle) return;
    sortables.delete(list);
    for (const item of Array.from(ending.keys())) finish(item);
    list.removeEventListener('keydown', onKey);
    list.removeEventListener('pointerdown', onDown);
    list.removeEventListener('pointermove', onMove);
    list.removeEventListener('pointerup', onUp);
    list.removeEventListener('pointercancel', onUp);
    },
    };
    sortables.set(list, { handle });
    return handle;
    }
    const VIEW_TYPES = Object.freeze(['fade-through', 'forward', 'back', 'container']);
    const viewCalls = new WeakMap();
    const isElement = (value) => value?.nodeType === 1;
    const liftedNames = new WeakMap();
    const MODAL_OPEN = [
    '.ds-dialog[data-state="open"]:not(.ds-dialog--contained)',
    '.ds-sheet[data-state="open"]',
    'dialog.ds-dialog[open]:not(.ds-dialog--contained)',
    'dialog.ds-sheet[open]',
    '.ds-dialog:not(dialog, [hidden], [data-state], .ds-dialog--contained, .ds-dialog--hidden, .ds-dialog--closing)',
    '.ds-sheet[aria-modal="true"]:not(dialog, [hidden], [data-state], .ds-sheet--closing)',
    ].join(', ');
    const MODAL_DRAWER = '(max-width: 900px)';
    const FRAME_PARTS = '.ds-view, .ds-panel, .ds-drawer';
    function liftName(doc, element) {
    let lifted = liftedNames.get(doc);
    if (lifted?.has(element)) return;
    if (!lifted) liftedNames.set(doc, (lifted = new Map()));
    lifted.set(element, element.style.getPropertyValue('view-transition-name'));
    element.style.setProperty('view-transition-name', 'none');
    }
    function liftIndicatorNames(doc) {
    for (const pill of Array.from(doc.querySelectorAll?.('.ds-indicator') ?? [])) {
    const name = pill.style.getPropertyValue('view-transition-name');
    if (name && name !== 'none') liftName(doc, pill);
    }
    }
    function modalDrawer(doc) {
    if (doc.defaultView?.matchMedia?.(MODAL_DRAWER)?.matches !== true) return false;
    if (doc.querySelector?.('.ds-app[data-drawer-open]')) return true;
    const drawer = doc.querySelector?.('.ds-app .ds-drawer');
    return Boolean(drawer?.getAnimations?.().some((animation) => animation.playState === 'running'));
    }
    function liftFrameNames(doc) {
    if (!doc.querySelector?.(MODAL_OPEN) && !modalDrawer(doc)) return;
    for (const part of Array.from(doc.querySelectorAll?.(FRAME_PARTS) ?? [])) liftName(doc, part);
    }
    function liftExtraToasts(doc) {
    const regions = Array.from(doc.querySelectorAll?.('.ds-toasts') ?? []);
    const shown = regions.filter((region) => Array.from(region.children ?? []).some((child) => child.classList?.contains('ds-toast')));
    for (const region of shown.slice(1)) liftName(doc, region);
    }
    const forwardedClicks = new WeakSet();
    function forwardPresses(doc, call) {
    const root = doc.documentElement;
    const target = doc.defaultView ?? doc;
    if (typeof target?.addEventListener !== 'function') return () => {};
    const land = (x, y) => {
    const running = viewCalls.get(doc);
    if (running) {
    running.transition?.skipTransition?.();
    running.done.then(() => land(x, y));
    return;
    }
    let element = doc.elementFromPoint?.(x, y) ?? null;
    while (element && typeof element.click !== 'function') element = element.parentElement;
    if (element && element !== root) element.click();
    };
    const onClick = ( event) => {
    if (event.target !== root || forwardedClicks.has(event)) return;
    forwardedClicks.add(event);
    event.stopPropagation();
    land(event.clientX, event.clientY);
    };
    const onPointerDown = ( event) => {
    if (event.target !== root || event.button !== 0 || viewCalls.get(doc) !== call) return;
    event.stopPropagation();
    call.transition?.skipTransition?.();
    target.addEventListener('click', onClick, { capture: true, once: true });
    };
    const onClickDuring = ( event) => {
    if (viewCalls.get(doc) === call) onClick(event);
    };
    target.addEventListener('pointerdown', onPointerDown, true);
    target.addEventListener('click', onClickDuring, true);
    return () => {
    target.removeEventListener('pointerdown', onPointerDown, true);
    target.removeEventListener('click', onClickDuring, true);
    };
    }
    function panelWidth(doc) {
    const panel = doc.querySelector?.('.ds-theme .ds-panel');
    return panel?.getBoundingClientRect?.().width ?? null;
    }
    function restoreLiftedNames(doc) {
    const lifted = liftedNames.get(doc);
    if (!lifted) return;
    liftedNames.delete(doc);
    for (const [element, before] of lifted) {
    if (element.style.getPropertyValue('view-transition-name') !== 'none') continue;
    if (before) element.style.setProperty('view-transition-name', before);
    else element.style.removeProperty('view-transition-name');
    }
    }
    function clippedOnScreen(element) {
    const doc = element.ownerDocument;
    const view = doc?.defaultView;
    if (typeof element.getBoundingClientRect !== 'function' || typeof view?.getComputedStyle !== 'function' || !(view.innerWidth > 0)) return false;
    const rect = element.getBoundingClientRect();
    const shown = {
    left: Math.max(rect.left, 0),
    top: Math.max(rect.top, 0),
    right: Math.min(rect.right, view.innerWidth),
    bottom: Math.min(rect.bottom, view.innerHeight),
    };
    if (shown.right - shown.left < 1 || shown.bottom - shown.top < 1) return true;
    const stop = new Set([doc.documentElement, doc.body]);
    for (let node = element; node && !stop.has(node); node = node.parentElement) {
    const style = view.getComputedStyle(node);
    if (node !== element) {
    const box = node.getBoundingClientRect();
    const left = box.left + (node.clientLeft || 0);
    const top = box.top + (node.clientTop || 0);
    if (style.overflowX !== 'visible' && (shown.left < left - 1 || shown.right > left + node.clientWidth + 1)) return true;
    if (style.overflowY !== 'visible' && (shown.top < top - 1 || shown.bottom > top + node.clientHeight + 1)) return true;
    }
    if (style.position === 'fixed') break;
    }
    return false;
    }
    function nameContainer(call, element) {
    if (!call.names.has(element)) call.names.set(element, element.style.viewTransitionName ?? '');
    element.style.viewTransitionName = 'ds-container';
    }
    function unnameContainer(call, element) {
    if (!call.names.has(element)) return;
    element.style.viewTransitionName = call.names.get(element);
    call.names.delete(element);
    }
    function unnameAll(call) {
    for (const element of Array.from(call.names.keys())) unnameContainer(call, element);
    }
    function swapTarget(doc, result) {
    if (isElement(result)) return result;
    const view = doc?.querySelector?.('.ds-theme .ds-vt-view') ?? doc?.querySelector?.('.ds-theme .ds-view');
    for (const child of Array.from(view?.children ?? [])) {
    if (child.hasAttribute?.('data-ds-keep') || child.getAttribute?.('aria-hidden') === 'true') continue;
    return child;
    }
    return null;
    }
    function moveViewFocus(doc, before, result, option) {
    if (!option || !doc) return;
    const now = doc.activeElement;
    const app = before?.closest?.('.ds-app');
    const fromDrawer = Boolean(before?.closest?.('.ds-drawer')) && Boolean(app) && !app.hasAttribute('data-drawer-open')
    && doc.defaultView?.matchMedia?.(MODAL_DRAWER)?.matches === true;
    const lost = !now || now === doc.body || now === doc.documentElement || (before && before.isConnected === false) || fromDrawer
    || Boolean(now.closest?.('[inert], [data-ds-leaving]'));
    if (!lost) return;
    const incoming = isElement(result) ? result : null;
    let target = null;
    if (typeof option === 'function') target = option(incoming);
    else {
    const root = incoming ?? doc.querySelector?.('.ds-theme .ds-vt-view') ?? doc.querySelector?.('.ds-theme .ds-view');
    target = (root?.matches?.('h1[tabindex="-1"]') ? root : root?.querySelector?.('h1[tabindex="-1"]'))
    ?? root?.closest?.('.ds-view, .ds-vt-view') ?? root;
    }
    if (target && typeof target.focus === 'function' && target.isConnected !== false && !target.closest?.('[inert]')) target.focus({ preventScroll: true });
    }
    function swapView(doc, update, focus) {
    const before = doc?.activeElement ?? null;
    let result;
    try {
    result = update();
    } catch (error) {
    return Promise.reject(error);
    }
    return Promise.resolve(result).then((value) => {
    resyncScroll(doc);
    moveViewFocus(doc, before, value, focus);
    const target = swapTarget(doc, value);
    return target ? enter(target, 'swap') : undefined;
    });
    }
    function viewTransition(doc, update, type, from, focus) {
    const root = doc.documentElement;
    const before = doc.activeElement;
    const previous = viewCalls.get(doc);
    if (previous) {
    previous.transition?.skipTransition?.();
    unnameAll(previous);
    }
    const container = type === 'container' && isElement(from) && from.isConnected && !clippedOnScreen(from) ? from : null;
    const call = { transition: null, updated: Promise.resolve(), done: Promise.resolve(), names: new Map() };
    const latest = () => viewCalls.get(doc) === call;
    viewCalls.set(doc, call);
    root.setAttribute('data-ds-vt', type === 'container' && !container ? 'fade-through' : type);
    root.removeAttribute('data-ds-vt-morph');
    if (container) nameContainer(call, container);
    liftIndicatorNames(doc);
    liftFrameNames(doc);
    liftExtraToasts(doc);
    let settleUpdate = () => {};
    call.updated = new Promise((resolve) => { settleUpdate = resolve; });
    let watchdog = 0;
    let failure = (null);
    const run = async () => {
    watchdog = (setTimeout(() => call.transition?.skipTransition?.(), motionDuration(root, '--ds-dur-long')));
    try {
    if (container) unnameContainer(call, container);
    if (previous) await previous.updated;
    const widthBefore = panelWidth(doc);
    let result;
    try {
    result = await update();
    } catch (error) {
    failure = { error };
    call.transition?.skipTransition?.();
    return;
    }
    if (latest()) liftIndicatorNames(doc);
    if (latest()) moveViewFocus(doc, before, result, focus);
    const widthAfter = latest() && widthBefore !== null ? panelWidth(doc) : null;
    if (widthAfter !== null && Math.abs(widthAfter - widthBefore) > 0.5) root.setAttribute('data-ds-vt-morph', '');
    if (type === 'container' && latest()) {
    if (container && isElement(result) && result.isConnected && !clippedOnScreen(result)) nameContainer(call, result);
    else root.setAttribute('data-ds-vt', 'fade-through');
    }
    } finally {
    clearTimeout(watchdog);
    settleUpdate();
    }
    };
    let started;
    try {
    started = doc.startViewTransition(run);
    } catch (error) {
    unnameAll(call);
    if (latest()) {
    root.removeAttribute('data-ds-vt');
    root.removeAttribute('data-ds-vt-morph');
    viewCalls.delete(doc);
    restoreLiftedNames(doc);
    }
    return swapView(doc, update, focus);
    }
    call.transition = started;
    call.updated.then(() => resyncScroll(doc));
    started.ready?.catch?.(() => {});
    const unforward = forwardPresses(doc, call);
    const ended = Promise.allSettled([started.updateCallbackDone, started.finished]).then(([updated]) => {
    clearTimeout(watchdog);
    unforward();
    unnameAll(call);
    if (latest()) {
    root.removeAttribute('data-ds-vt');
    root.removeAttribute('data-ds-vt-morph');
    viewCalls.delete(doc);
    restoreLiftedNames(doc);
    }
    return updated;
    });
    call.done = ended.then(() => {});
    return ended.then((updated) => {
    if (failure) throw failure.error;
    if (updated.status === 'rejected') throw updated.reason;
    });
    }
    function transition(update, options = {}) {
    const type = options?.type ?? 'fade-through';
    const from = options?.from ?? null;
    if (!VIEW_TYPES.includes(type)) throw new TypeError(`DS.motion.transition: unknown type "${type}" (${VIEW_TYPES.join(', ')})`);
    if (typeof update !== 'function') throw new TypeError('DS.motion.transition: update must be a function');
    const doc = from?.ownerDocument ?? globalThis.document;
    const root = doc?.documentElement;
    const focus = options?.focus ?? false;
    if (doc && typeof doc.startViewTransition === 'function' && root?.classList?.contains('ds-theme')) return viewTransition(doc, update, type, from, focus);
    return swapView(doc, update, focus);
    }

    return Object.freeze({
      MOTION_FALLBACKS: MOTION_FALLBACKS,
      LOADER_DELAY_MS: LOADER_DELAY_MS,
      LOADER_MIN_VISIBLE_MS: LOADER_MIN_VISIBLE_MS,
      STAGGER_MAX: STAGGER_MAX,
      motionDuration: motionDuration,
      motionEasing: motionEasing,
      travelDuration: travelDuration,
      resyncScroll: resyncScroll,
      stagger: stagger,
      replay: replay,
      settled: settled,
      enter: enter,
      leave: leave,
      cancel: cancel,
      enterAll: enterAll,
      staggerOnce: staggerOnce,
      loaderHoldMs: loaderHoldMs,
      settleLoader: settleLoader,
      refresh: refresh,
      guard: guard,
      setLabel: setLabel,
      busy: busy,
      COUNT_UP_MS: COUNT_UP_MS,
      countUp: countUp,
      originFrom: originFrom,
      roll: roll,
      crossfade: crossfade,
      flip: flip,
      sortable: sortable,
      transition: transition
    });
  });

  add('indicator', ['OVERSHOOT', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit;
    const motionDuration = DS.motion.motionDuration, travelDuration = DS.motion.travelDuration;
    const OVERSHOOT = 0.05;
    const EDGES = ['--ds-indicator-t', '--ds-indicator-r', '--ds-indicator-b', '--ds-indicator-l'];
    const ACTIVE_ATTRIBUTES = ['aria-current', 'aria-pressed', 'aria-selected', 'aria-checked'];
    const INSTANT = 'ds-indicator--instant';
    const MODAL_OPEN = [
    '.ds-dialog[data-state="open"]:not(.ds-dialog--contained)',
    '.ds-sheet[data-state="open"]',
    'dialog.ds-dialog[open]:not(.ds-dialog--contained)',
    'dialog.ds-sheet[open]',
    '.ds-dialog:not(dialog, [hidden], [data-state], .ds-dialog--contained, .ds-dialog--hidden, .ds-dialog--closing)',
    '.ds-sheet[aria-modal="true"]:not(dialog, [hidden], [data-state], .ds-sheet--closing)',
    ].join(', ');
    const live = new WeakMap();
    const fontWatchers = new WeakMap();
    const pageWatchers = new WeakMap();
    function notify(set, method, event) {
    for (const ref of set) {
    const watcher = ref.deref();
    if (!watcher || watcher.gone()) set.delete(ref);
    else watcher[method](event);
    }
    }
    function watch(registry, owner, watcher, listen) {
    let entry = registry.get(owner);
    if (!entry) {
    const set = new Set();
    entry = { set, stop: listen(set) };
    registry.set(owner, entry);
    }
    const ref = new WeakRef(watcher);
    entry.set.add(ref);
    return () => {
    entry.set.delete(ref);
    if (entry.set.size || registry.get(owner) !== entry) return;
    registry.delete(owner);
    entry.stop();
    };
    }
    const same = (a, b) => Boolean(a && b) && ['left', 'top', 'right', 'bottom'].every((side) => Math.abs(a[side] - b[side]) < 0.5);
    const px = (value) => `${value}px`;
    const kept = (node) => node?.nodeType === 1 && node.hasAttribute?.('data-ds-keep');
    function rendered(item) {
    if (item.closest?.('details:not([open]) > :not(summary)')) return false;
    return typeof item.getClientRects !== 'function' || item.getClientRects().length > 0;
    }
    function optionsKey({ active, className = '', observe = true, name = '', reveal = false }) {
    return JSON.stringify([active, className, Boolean(observe), name, Boolean(reveal)]);
    }
    function activeState(current, selector, item) {
    const named = ACTIVE_ATTRIBUTES.filter((name) => selector.includes(`[${name}`));
    const says = (name) => {
    const value = current?.getAttribute(name);
    return value && value !== 'false' ? value : null;
    };
    const own = named.find(says);
    if (own) return { name: own, value: says(own) };
    const name = named.find((other) => item?.hasAttribute(other)) ?? named[0];
    if (!name) return null;
    const value = selector.match(new RegExp(`\\[${name}=["']?([^"'\\]]+)`))?.[1];
    return { name, value: value && value !== 'false' ? value : 'true' };
    }
    function makePill(track, options) {
    const { active, className = '', observe = true, name = '', reveal = false } = options;
    const doc = track.ownerDocument ?? document;
    const view = doc.defaultView ?? globalThis;
    const indicator = doc.createElement('span');
    indicator.className = `ds-indicator ${INSTANT}${className ? ` ${className}` : ''}`;
    indicator.setAttribute('aria-hidden', 'true');
    indicator.setAttribute('data-ds-keep', '');
    if (name) indicator.style.setProperty('view-transition-name', `ds-indicator-${name}`);
    track.prepend(indicator);
    const settling = !track.hasAttribute('data-ds-moving');
    if (settling) track.setAttribute('data-ds-moving', '');
    track.classList.add('ds-has-indicator');
    if (settling) {
    if (typeof view.getComputedStyle === 'function') void view.getComputedStyle(track).opacity;
    track.removeAttribute('data-ds-moving');
    }
    let announced;
    let placed = (null);
    let target = (null);
    let box = target;
    let jump = true;
    let generation = 0;
    let destroyed = false;
    let frame = 0;
    let adopted = false;
    const computed = () => (typeof view.getComputedStyle === 'function' ? view.getComputedStyle(indicator) : null);
    const flush = () => computed()?.getPropertyValue(EDGES[0]);
    function edgeTransitions() {
    if (typeof indicator.getAnimations !== 'function') return [];
    return indicator.getAnimations().filter((animation) => EDGES.includes(animation.transitionProperty));
    }
    function measure(item, trackRect) {
    const rect = item.getBoundingClientRect();
    const left = rect.left - trackRect.left - track.clientLeft + track.scrollLeft;
    const top = rect.top - trackRect.top - track.clientTop + track.scrollTop;
    return { left, top, right: left + rect.width, bottom: top + rect.height };
    }
    function painted() {
    const style = computed();
    if (!box || !style) return target;
    const edge = (index) => parseFloat(style.getPropertyValue(EDGES[index])) || 0;
    return { left: box.left + edge(3), top: box.top + edge(0), right: box.right - edge(1), bottom: box.bottom - edge(2) };
    }
    function write(nextBox, rect) {
    box = nextBox;
    const style = indicator.style;
    style.setProperty('--ds-indicator-x', px(nextBox.left));
    style.setProperty('--ds-indicator-y', px(nextBox.top));
    style.setProperty('--ds-indicator-w', px(nextBox.right - nextBox.left));
    style.setProperty('--ds-indicator-h', px(nextBox.bottom - nextBox.top));
    insets(rect);
    }
    function insets(rect) {
    const style = indicator.style;
    style.setProperty(EDGES[0], px(rect.top - box.top));
    style.setProperty(EDGES[1], px(box.right - rect.right));
    style.setProperty(EDGES[2], px(box.bottom - rect.bottom));
    style.setProperty(EDGES[3], px(rect.left - box.left));
    }
    function prepare(nextBox, from) {
    indicator.classList.add(INSTANT);
    delete indicator.dataset.dir;
    indicator.dataset.glide = '';
    indicator.style.setProperty('border-radius', '0px');
    write(nextBox, from);
    flush();
    }
    function release(gen, done) {
    indicator.classList.remove(INSTANT);
    const running = edgeTransitions();
    if (!running.length) {
    done();
    return;
    }
    Promise.allSettled(running.map((animation) => animation.finished)).then(() => {
    if (!destroyed && gen === generation) done();
    });
    }
    function glideBox(from, to) {
    const margin = (travel) => (travel > 0 ? Math.ceil(OVERSHOOT * travel) + 1 : 0);
    const union = {
    left: Math.min(from.left, to.left) - margin(from.left - to.left),
    top: Math.min(from.top, to.top) - margin(from.top - to.top),
    right: Math.max(from.right, to.right) + margin(to.right - from.right),
    bottom: Math.max(from.bottom, to.bottom) + margin(to.bottom - from.bottom),
    };
    const style = typeof view.getComputedStyle === 'function' ? view.getComputedStyle(track) : null;
    if (style && style.overflowX !== 'visible') {
    union.left = Math.max(union.left, Math.min(from.left, to.left, 0));
    union.right = Math.min(union.right, Math.max(from.right, to.right, track.scrollWidth));
    }
    if (style && style.overflowY !== 'visible') {
    union.top = Math.max(union.top, Math.min(from.top, to.top, 0));
    union.bottom = Math.min(union.bottom, Math.max(from.bottom, to.bottom, track.scrollHeight));
    }
    return union;
    }
    function adoptRadius(rect) {
    const style = computed();
    if (!style || (!adopted && style.getPropertyValue('--ds-indicator-radius').trim() !== '0px')) return;
    const own = String(style.borderTopLeftRadius || '').trim().split(/\s+/)[0];
    const side = Math.min(rect.right - rect.left, rect.bottom - rect.top);
    const value = parseFloat(own);
    const radius = Math.min(own.endsWith('%') ? value * side * 0.01 : value, side / 2);
    if (!(radius > 0)) {
    if (adopted) indicator.style.removeProperty('--ds-indicator-radius');
    adopted = false;
    return;
    }
    const copy = `calc(${px(radius)} * var(--ds-shape, 1))`;
    adopted = true;
    if (indicator.style.getPropertyValue('--ds-indicator-radius') === copy) return;
    indicator.style.setProperty('transition', 'none');
    indicator.style.setProperty('--ds-indicator-radius', copy);
    flush();
    indicator.style.removeProperty('transition');
    }
    function still() {
    indicator.classList.add(INSTANT);
    delete indicator.dataset.glide;
    delete indicator.dataset.dir;
    indicator.style.removeProperty('border-radius');
    }
    function rest(item, rect) {
    generation += 1;
    still();
    delete indicator.dataset.empty;
    write(rect, rect);
    target = rect;
    jump = false;
    adoptRadius(rect);
    setPlaced(item);
    }
    function setPlaced(item) {
    if (placed === item) return;
    placed = item;
    schedule();
    }
    function labelTime() {
    const own = parseFloat(track.style.getPropertyValue('--ds-indicator-dur'));
    return Number.isFinite(own) ? own : motionDuration(track, '--ds-dur-medium');
    }
    function setTime(duration) {
    const previous = labelTime();
    track.style.setProperty('--ds-indicator-dur', `${duration}ms`);
    if (typeof track.getAnimations !== 'function' || Math.abs(previous - duration) < 0.5) return;
    const fresh = motionDuration(track, '--ds-dur-micro');
    for (const animation of track.getAnimations({ subtree: true })) {
    if (animation.transitionProperty !== 'color' || animation.effect?.target === indicator) continue;
    if (Math.abs(Number(animation.effect.getTiming().duration) - previous) >= 0.5) continue;
    if (Number(animation.currentTime ?? 0) > fresh) continue;
    animation.effect.updateTiming({ duration });
    }
    }
    function glide(item, from, to) {
    const gen = ++generation;
    const dx = (to.left + to.right - from.left - from.right) / 2;
    const dy = (to.top + to.bottom - from.top - from.bottom) / 2;
    const distance = Math.hypot(dx, dy);
    setTime(travelDuration(track, distance));
    prepare(glideBox(from, to), from);
    if (distance >= 0.5) {
    indicator.dataset.dir = Math.abs(dx) >= Math.abs(dy) ? (dx < 0 ? 'left' : 'right') : (dy < 0 ? 'up' : 'down');
    }
    insets(to);
    target = to;
    setPlaced(item);
    release(gen, () => rest(item, to));
    }
    function appear(item, to) {
    if (edgeTransitions().length) {
    const from = painted();
    delete indicator.dataset.empty;
    glide(item, from, to);
    return;
    }
    const gen = ++generation;
    setTime(motionDuration(track, '--ds-dur-medium'));
    indicator.dataset.empty = '';
    const half = (to.right - to.left) / 2;
    prepare(to, { left: to.left + half, top: to.top, right: to.right - half, bottom: to.bottom });
    delete indicator.dataset.empty;
    insets(to);
    target = to;
    setPlaced(item);
    release(gen, () => rest(item, to));
    }
    function disappear() {
    const gen = ++generation;
    const from = edgeTransitions().length ? painted() : target;
    placed = null;
    target = null;
    if (!from || jump) {
    still();
    indicator.dataset.empty = '';
    jump = false;
    return;
    }
    prepare(box, from);
    indicator.dataset.empty = '';
    const half = (from.right - from.left) / 2;
    insets({ left: from.left + half, top: from.top, right: from.right - half, bottom: from.bottom });
    release(gen, still);
    }
    function room() {
    const style = typeof view.getComputedStyle === 'function' ? view.getComputedStyle(track) : null;
    const px = (name) => (style ? parseFloat(style.getPropertyValue(name)) : NaN);
    const space = px('--ds-space-2');
    const base = Number.isFinite(space) ? space : 8;
    const side = (name) => Math.max(base, px(name) || 0);
    return { top: side('scroll-padding-top'), bottom: side('scroll-padding-bottom'), left: side('scroll-padding-left'), right: side('scroll-padding-right') };
    }
    function revealRect(rect, instant) {
    const margin = room();
    const axis = (start, end, scroll, size, range, before, after) => {
    if (range <= size + 1) return scroll;
    if (start - before < scroll || end - start + before + after > size) return Math.max(0, start - before);
    if (end + after > scroll + size) return end + after - size;
    return scroll;
    };
    const top = axis(rect.top, rect.bottom, track.scrollTop, track.clientHeight, track.scrollHeight, margin.top, margin.bottom);
    const left = axis(rect.left, rect.right, track.scrollLeft, track.clientWidth, track.scrollWidth, margin.left, margin.right);
    if (Math.abs(top - track.scrollTop) < 0.5 && Math.abs(left - track.scrollLeft) < 0.5) return;
    if (typeof track.scrollTo === 'function') track.scrollTo({ top, left, behavior: instant ? 'auto' : 'smooth' });
    }
    function place() {
    if (destroyed) return;
    if (track.isConnected === false) return;
    if (indicator.parentNode !== track) {
    track.prepend(indicator);
    jump = true;
    }
    const trackRect = track.getBoundingClientRect();
    if (trackRect.width === 0) {
    generation += 1;
    still();
    indicator.dataset.empty = '';
    placed = null;
    target = null;
    jump = true;
    return;
    }
    if (reveal && track.classList.contains('ds-drawer__list')) {
    track.classList.toggle('ds-drawer__list--overflow', track.scrollHeight > track.clientHeight + 1);
    }
    const found = track.querySelector(active);
    move(found && rendered(found) ? found : null, trackRect);
    if (found === announced) return;
    const first = announced === undefined;
    announced = found;
    if (!first) emit(track, 'change', { item: found });
    }
    function move(item, trackRect) {
    if (!item) {
    if (placed !== null || jump) disappear();
    return;
    }
    const to = measure(item, trackRect);
    if (reveal) revealRect(to, jump);
    if (jump) {
    rest(item, to);
    return;
    }
    if (placed === null) {
    appear(item, to);
    return;
    }
    const running = edgeTransitions().length > 0;
    if (running && same(to, target)) return;
    if (running || item !== placed) {
    glide(item, running ? painted() : target, to);
    return;
    }
    if (!same(to, target)) rest(item, to);
    }
    let observed = new Set();
    const resize = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(() => place()) : null;
    function syncObserved() {
    if (!resize) return;
    const wanted = new Set([track]);
    for (const child of track.children ?? []) if (child !== indicator) wanted.add(child);
    if (placed) wanted.add(placed);
    for (const element of observed) if (!wanted.has(element)) resize.unobserve(element);
    for (const element of wanted) if (!observed.has(element)) resize.observe(element);
    observed = wanted;
    }
    function relevant(record) {
    if (record.target === indicator) return false;
    const keep = record.target?.closest?.('[data-ds-keep]');
    if (keep && keep !== track && track.contains(keep)) return false;
    if (record.type !== 'childList') return true;
    return ![...record.addedNodes, ...record.removedNodes].every(kept);
    }
    const mutations = observe && typeof view.MutationObserver === 'function'
    ? new view.MutationObserver((records) => {
    if (records.some(relevant)) schedule();
    })
    : null;
    mutations?.observe(track, { subtree: true, childList: true, attributes: true, attributeFilter: ACTIVE_ATTRIBUTES });
    function schedule() {
    if (destroyed || frame || typeof view.requestAnimationFrame !== 'function') return;
    frame = view.requestAnimationFrame(() => {
    frame = 0;
    if (destroyed) return;
    syncObserved();
    place();
    });
    }
    function shownWhole() {
    const rect = indicator.getBoundingClientRect();
    let clip = { left: 0, top: 0, right: view.innerWidth ?? Infinity, bottom: view.innerHeight ?? Infinity };
    for (let el = indicator.parentElement; el && el !== doc.body && el !== doc.documentElement; el = el.parentElement) {
    const cs = view.getComputedStyle(el);
    if (cs.overflowX === 'visible' && cs.overflowY === 'visible') continue;
    const outer = el.getBoundingClientRect();
    const left = outer.left + el.clientLeft;
    const top = outer.top + el.clientTop;
    clip = { left: Math.max(clip.left, left), top: Math.max(clip.top, top), right: Math.min(clip.right, left + el.clientWidth), bottom: Math.min(clip.bottom, top + el.clientHeight) };
    }
    return rect.left >= clip.left - 0.5 && rect.top >= clip.top - 0.5 && rect.right <= clip.right + 0.5 && rect.bottom <= clip.bottom + 0.5;
    }
    const ownName = name ? `ds-indicator-${name}` : '';
    const rename = () => {
    if (!destroyed) indicator.style.setProperty('view-transition-name', ownName);
    };
    const covered = () => Boolean(doc.querySelector?.(`${MODAL_OPEN}, .ds-app[data-drawer-open]`));
    const onPageTransition = (event) => {
    if (destroyed || !event.viewTransition || typeof view.getComputedStyle !== 'function') return;
    if (!covered() && shownWhole()) return;
    indicator.style.setProperty('view-transition-name', 'none');
    event.viewTransition.finished?.then(rename, rename);
    };
    const onPageShow = (event) => {
    if (event.persisted) rename();
    };
    const watcher = {
    fonts: () => schedule(),
    page: onPageTransition,
    shown: onPageShow,
    gone: () => destroyed,
    };
    const unwatch = [];
    const fonts = doc.fonts;
    if (fonts) {
    unwatch.push(watch(fontWatchers, fonts, watcher, (set) => {
    const onFonts = () => notify(set, 'fonts');
    fonts.addEventListener?.('loadingdone', onFonts);
    return () => fonts.removeEventListener?.('loadingdone', onFonts);
    }));
    fonts.ready?.then(() => watcher.fonts()).catch(() => {});
    }
    if (name) {
    unwatch.push(watch(pageWatchers, view, watcher, (set) => {
    const onTransition = (event) => notify(set, 'page', event);
    const onShow = (event) => notify(set, 'shown', event);
    view.addEventListener?.('pageswap', onTransition);
    view.addEventListener?.('pagereveal', onTransition);
    view.addEventListener?.('pageshow', onShow);
    return () => {
    view.removeEventListener?.('pageswap', onTransition);
    view.removeEventListener?.('pagereveal', onTransition);
    view.removeEventListener?.('pageshow', onShow);
    };
    }));
    }
    place();
    syncObserved();
    return {
    watcher,
    place,
    get: () => track.querySelector(active),
    set(value) {
    const item = typeof value === 'string' ? track.querySelector(value) : value ?? null;
    if (value !== null && (!item || !track.contains(item))) return;
    const current = [...track.querySelectorAll(active)];
    if (item ? current.length === 1 && current[0] === item : !current.length) return;
    const state = activeState(current[0], active, item);
    if (!state) return;
    for (const other of current) {
    if (state.name === 'aria-current') other.removeAttribute(state.name);
    else other.setAttribute(state.name, 'false');
    }
    item?.setAttribute(state.name, state.value);
    place();
    },
    destroy() {
    if (destroyed) return;
    destroyed = true;
    generation += 1;
    resize?.disconnect();
    mutations?.disconnect();
    if (frame) view.cancelAnimationFrame?.(frame);
    frame = 0;
    for (const stop of unwatch) stop();
    indicator.remove();
    track.classList.remove('ds-has-indicator');
    track.style.removeProperty('--ds-indicator-dur');
    },
    };
    }
    function replacePill(entry, track, options, key) {
    entry.pill.destroy();
    entry.pill = makePill(track, options);
    entry.key = key;
    }
    function openPill(track, options, read) {
    const key = optionsKey(options);
    const existing = live.get(track);
    if (existing) {
    if (read) existing.read = read;
    if (existing.key === key) return existing.handle;
    if (existing.read) {
    replacePill(existing, track, options, key);
    return existing.handle;
    }
    existing.handle.destroy();
    }
    const entry = { key, read, pill: makePill(track, options), stops: new Set(), gone: false, handle: null };
    entry.handle = {
    get: () => entry.pill.get(),
    set(value) {
    entry.pill.set(value);
    },
    on(type, fn) {
    if (entry.gone) return () => {};
    const name = String(type).startsWith('ds:') ? String(type) : `ds:${type}`;
    const listener = (event) => {
    if (event.target === track) fn(event);
    };
    const stop = () => {
    track.removeEventListener(name, listener);
    entry.stops.delete(stop);
    };
    track.addEventListener(name, listener);
    entry.stops.add(stop);
    return stop;
    },
    update(names) {
    if (entry.gone) return;
    if (names?.length && entry.read) {
    const next = entry.read();
    const nextKey = optionsKey(next);
    if (nextKey !== entry.key) replacePill(entry, track, next, nextKey);
    return;
    }
    entry.pill.place();
    },
    destroy() {
    if (entry.gone) return;
    entry.gone = true;
    entry.pill.destroy();
    for (const stop of [...entry.stops]) stop();
    if (live.get(track) === entry) live.delete(track);
    },
    };
    live.set(track, entry);
    return entry.handle;
    }
    function create(track, options) {
    return openPill(track, options, null);
    }
    const ANY_ACTIVE = '[aria-current]:not([aria-current="false"]), [aria-pressed="true"], [aria-selected="true"], [aria-checked="true"]';
    define('indicator', {
    attribute: 'data-ds-indicator',
    attributes: ['data-ds-indicator-class', 'data-ds-indicator-name', 'data-ds-indicator-reveal'],
    init(el) {
    const read = () => ({
    active: el.getAttribute('data-ds-indicator') || ANY_ACTIVE,
    className: el.getAttribute('data-ds-indicator-class') ?? '',
    name: el.getAttribute('data-ds-indicator-name') ?? '',
    reveal: el.hasAttribute('data-ds-indicator-reveal'),
    });
    return openPill(el, read(), read);
    },
    });

    return Object.freeze({
      OVERSHOOT: OVERSHOOT,
      create: create
    });
  });

  add('dom', ['morphChildren', 'morphHtml', 'own', 'disown', 'watchScrolled', 'scrollEdges', 'progress', 'roving', 'typesText', 'followViewport', 'followEnd', 'autosize', 'dropzone'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const resyncScroll = DS.motion.resyncScroll;
    const LOOKAHEAD = 8;
    const RUNTIME_CLASS = /^ds-(?:enter|leave)(?:--[\w-]+)?$|^ds-(?:has-indicator|pressed|ripples)$/;
    const RUNTIME_STYLE = /^--ds-(?:i|indicator-[\w-]+|ripple-[xyr]|motion-origin|clip-from)$/;
    const RUNTIME_ATTRIBUTES = new Set(['data-ds-staggered', 'data-ds-moving']);
    const OWNED = Symbol.for('ds.owned');
    const owned = (element, kind, name) => Boolean(element[OWNED]?.[kind]?.get(name));
    const exiting = (element) => Array.from(element.classList ?? []).some((name) => /^ds-leave(?:--[\w-]+)?$/.test(name));
    function activeIn(node) {
    const root = node?.getRootNode?.();
    return (root?.nodeType === 11 ? root.activeElement : null) ?? node?.ownerDocument?.activeElement ?? null;
    }
    const isElement = (node) => node?.nodeType === 1;
    function declarations(text) {
    const out = new Map();
    for (const part of String(text ?? '').split(';')) {
    const colon = part.indexOf(':');
    if (colon < 0) continue;
    const name = part.slice(0, colon).trim();
    if (name) out.set(name.startsWith('--') ? name : name.toLowerCase(), part.slice(colon + 1).trim().replace(/\s+/g, ' '));
    }
    return out;
    }
    function equivalentShell(a, b) {
    if (!isElement(a) || !isElement(b) || a.tagName !== b.tagName) return false;
    const classes = (element, other) => new Set((element.getAttribute('class') ?? '').split(/\s+/)
    .filter((name) => name && !RUNTIME_CLASS.test(name) && !owned(other, 'classes', name)));
    const ca = classes(a, a);
    const cb = classes(b, a);
    if (ca.size !== cb.size || [...ca].some((name) => !cb.has(name))) return false;
    const styles = (element) => new Map([...declarations(element.getAttribute('style'))]
    .filter(([name]) => !RUNTIME_STYLE.test(name) && !owned(a, 'props', name)));
    const sa = styles(a);
    const sb = styles(b);
    if (sa.size !== sb.size || [...sa].some(([name, value]) => sb.get(name) !== value)) return false;
    const others = (element) => Array.from(element.attributes).filter(({ name }) => name !== 'class' && name !== 'style'
    && !RUNTIME_ATTRIBUTES.has(name) && !owned(a, 'attrs', name) && !(name === 'inert' && exiting(a)));
    const oa = others(a);
    const ob = others(b);
    return oa.length === ob.length && oa.every(({ name, value }) => b.getAttribute(name) === value);
    }
    function sameShell(a, b) {
    if (!isElement(a) || !isElement(b)) return false;
    const x = (a);
    const y = (b);
    if (x.tagName !== y.tagName || x.attributes.length !== y.attributes.length) return false;
    for (const attribute of Array.from(x.attributes)) {
    if (y.getAttribute(attribute.name) !== attribute.value) return false;
    }
    return true;
    }
    function keyOf(node) {
    if (!isElement(node)) return null;
    return node.getAttribute('data-ds-key') || node.getAttribute('id') || null;
    }
    const kept = (node) => isElement(node) && node.hasAttribute('data-ds-keep');
    const leaving = (node) => isElement(node) && node.hasAttribute('data-ds-leaving');
    const skipped = (node) => kept(node) || leaving(node);
    const blank = (node) => node.nodeType === 3 && !/\S/.test(node.nodeValue ?? '');
    const replacing = (node) => isElement(node) && node.hasAttribute('data-ds-replace');
    function live(node) {
    let current = node;
    while (current && skipped(current)) current = current.nextSibling;
    return current;
    }
    function holdsFocus(node, ctx) {
    return Boolean(ctx.active && isElement(node) && (node === ctx.active || node.contains(ctx.active)));
    }
    function sameKind(a, b) {
    if (a.nodeType !== b.nodeType) return false;
    return !isElement(a) || a.tagName === b.tagName;
    }
    function removeNode(node, ctx) {
    if (kept(node)) return;
    if (!isElement(node) || typeof ctx.options.onRemove !== 'function') {
    node.remove();
    return;
    }
    const result = ctx.options.onRemove( (node));
    if (!result || typeof ( (result).then) !== 'function') {
    node.remove();
    return;
    }
    const element = (node);
    element.setAttribute('data-ds-leaving', '');
    element.setAttribute('aria-hidden', 'true');
    element.inert = true;
    const done = () => element.remove();
    (result).then(done, done);
    }
    function insertNode(parent, node, before, ctx) {
    parent.insertBefore(node, before);
    if (isElement(node)) ctx.options.onInsert?.(node);
    }
    function moveNode(parent, node, before, ctx) {
    if (typeof parent.moveBefore === 'function') {
    try {
    parent.moveBefore(node, before);
    return;
    } catch {
    }
    }
    const focused = holdsFocus(node, ctx) ? (ctx.active) : null;
    let selection = null;
    try {
    if (focused && typeof focused.selectionStart === 'number') selection = [focused.selectionStart, focused.selectionEnd, focused.selectionDirection];
    } catch {
    }
    if (!node.hasAttribute('data-ds-moving')) {
    node.setAttribute('data-ds-moving', '');
    ctx.moved.push(node);
    }
    parent.insertBefore(node, before);
    const doc = parent.ownerDocument;
    if (focused && doc && activeIn(focused) !== focused && typeof focused.focus === 'function') {
    focused.focus({ preventScroll: true });
    if (selection) focused.setSelectionRange?.(selection[0], selection[1], selection[2] ?? undefined);
    }
    }
    function syncClass(old, next) {
    const current = Array.from(old.classList);
    const wanted = (next.getAttribute('class') ?? '').split(/\s+/).filter(Boolean)
    .filter((name) => current.includes(name) || !owned(old, 'classes', name));
    const runtime = current.filter((name) => (RUNTIME_CLASS.test(name) || owned(old, 'classes', name)) && !wanted.includes(name));
    const result = [...wanted, ...runtime];
    if (result.length === current.length && result.every((name) => current.includes(name))) return;
    if (result.length) old.setAttribute('class', result.join(' '));
    else old.removeAttribute('class');
    }
    function syncStyle(old, next) {
    const wanted = next.getAttribute('style');
    if (old.getAttribute('style') === wanted) return;
    const style = old.style;
    if (!style || typeof style.setProperty !== 'function') {
    if (wanted === null) old.removeAttribute('style');
    else old.setAttribute('style', wanted);
    return;
    }
    const runtime = [];
    const props = old[OWNED]?.props;
    const absent = [];
    for (let index = 0; index < style.length; index += 1) {
    const name = style[index];
    if (RUNTIME_STYLE.test(name) || owned(old, 'props', name)) runtime.push([name, style.getPropertyValue(name), style.getPropertyPriority(name)]);
    }
    for (const [name, count] of props ?? []) if (count && !style.getPropertyValue(name)) absent.push(name);
    style.cssText = wanted ?? '';
    for (const name of absent) style.removeProperty(name);
    for (const [name, value, priority] of runtime) style.setProperty(name, value, priority);
    if (!old.style.length) old.removeAttribute('style');
    }
    function isRadio(node) {
    return node?.tagName === 'INPUT' && node.type === 'radio';
    }
    function sameGroup(a, b) {
    return Boolean(a.name) && a.name === b.name && a.form === b.form && a.getRootNode() === b.getRootNode();
    }
    function inFocusedControl(element, active) {
    if (!active) return false;
    if (element === active) return true;
    if (element.tagName === 'OPTION') return element.closest('select') === active;
    return isRadio(element) && isRadio(active) && sameGroup(element, active);
    }
    function keepChoice(active) {
    if (active?.tagName === 'SELECT') {
    const select = (active);
    const before = new Map(Array.from(select.options, (option) => [option, option.selected]));
    return () => {
    const options = Array.from(select.options).filter((option) => before.has(option));
    if (select.multiple) for (const option of options) if (!before.get(option) && option.selected) option.selected = false;
    for (const option of options) if (before.get(option) && !option.selected) option.selected = true;
    };
    }
    if (isRadio(active) && (active).name) {
    const group = () => Array.from(active.getRootNode().querySelectorAll('input[type="radio"]')).filter((radio) => sameGroup(radio, active));
    const checked = group().find((radio) => radio.checked) ?? null;
    return () => {
    if (checked?.isConnected) {
    if (!checked.checked) checked.checked = true;
    } else if (!checked) {
    for (const radio of group()) if (radio.checked) radio.checked = false;
    }
    };
    }
    return null;
    }
    function syncProperty(element, name, present, ctx) {
    if (inFocusedControl(element, ctx.active)) return;
    const tag = element.tagName;
    if (tag === 'INPUT' && name === 'value') element.value = element.getAttribute('value') ?? '';
    else if (tag === 'INPUT' && name === 'checked') element.checked = present;
    else if (tag === 'OPTION' && name === 'selected') element.selected = present;
    }
    function syncAttributes(old, next, ctx) {
    for (const attribute of Array.from(old.attributes)) {
    const name = attribute.name;
    if (name === 'class' || name === 'style' || RUNTIME_ATTRIBUTES.has(name) || owned(old, 'attrs', name)) continue;
    if (name === 'inert' && exiting(old)) continue;
    if (attribute.namespaceURI ? next.hasAttributeNS(attribute.namespaceURI, attribute.localName) : next.hasAttribute(name)) continue;
    if (attribute.namespaceURI) old.removeAttributeNS(attribute.namespaceURI, attribute.localName);
    else old.removeAttribute(name);
    syncProperty(old, name, false, ctx);
    }
    for (const attribute of Array.from(next.attributes)) {
    const name = attribute.name;
    if (name === 'class' || name === 'style' || owned(old, 'attrs', name)) continue;
    if (attribute.namespaceURI) {
    if (old.getAttributeNS(attribute.namespaceURI, attribute.localName) === attribute.value) continue;
    old.setAttributeNS(attribute.namespaceURI, name, attribute.value);
    } else {
    if (old.getAttribute(name) === attribute.value) continue;
    old.setAttribute(name, attribute.value);
    }
    syncProperty(old, name, true, ctx);
    }
    syncClass(old, next);
    syncStyle(old, next);
    }
    function patchNode(old, next, ctx) {
    if (!isElement(old)) {
    if (old.nodeValue !== next.nodeValue) old.nodeValue = next.nodeValue;
    return;
    }
    if (!sameShell(old, next)) syncAttributes(old, next, ctx);
    const inner = { ...ctx, replace: ctx.replace || replacing(old) || replacing(next) };
    const before = old.tagName === 'TEXTAREA' ? old.textContent : null;
    patchChildren(old, Array.from(next.childNodes), inner, false);
    if (before !== null && old !== ctx.active && old.textContent !== before) old.value = old.textContent;
    ctx.options.onKeep?.(old, next);
    }
    function patchable(old, next, ctx) {
    if (!sameKind(old, next)) return false;
    if (holdsFocus(old, ctx)) return true;
    return !(ctx.replace || replacing(old) || replacing(next));
    }
    const shellOf = (old, next) => sameShell(old, next) || equivalentShell(old, next);
    function lookAhead(cursor, node, ctx) {
    let candidate = cursor.nextSibling;
    let checked = 0;
    if (holdsFocus(cursor, ctx)) return null;
    while (candidate && checked < LOOKAHEAD) {
    if (!skipped(candidate) && !blank(candidate) && keyOf(candidate) === null) {
    checked += 1;
    if (sameKind(candidate, node) && candidate.isEqualNode(node)) return candidate;
    if (holdsFocus(candidate, ctx)) return null;
    }
    candidate = candidate.nextSibling;
    }
    return null;
    }
    function patchInPlace(parent, desired, ctx) {
    const keyed = new Map();
    const unkeyed = [];
    for (const child of Array.from(parent.childNodes)) {
    if (skipped(child)) continue;
    const key = keyOf(child);
    if (key === null) unkeyed.push(child);
    else if (!keyed.has(key)) keyed.set(key, child);
    }
    let index = 0;
    for (const node of desired) {
    const key = keyOf(node);
    if (key !== null) {
    const old = keyed.get(key);
    if (old && old.tagName === node.tagName) {
    keyed.delete(key);
    patchNode(old, node, ctx);
    }
    continue;
    }
    const limit = Math.min(unkeyed.length, index + LOOKAHEAD + 1);
    for (let at = index; at < limit; at += 1) {
    if (!sameKind(unkeyed[at], node)) continue;
    if (!unkeyed[at].isEqualNode(node)) patchNode(unkeyed[at], node, ctx);
    index = at + 1;
    break;
    }
    }
    }
    function patchChildren(parent, desired, ctx, top) {
    if (top && ctx.options.holdOrder) {
    patchInPlace(parent, desired, ctx);
    return;
    }
    const keyed = new Map();
    for (const child of Array.from(parent.childNodes)) {
    const key = skipped(child) ? null : keyOf(child);
    if (key !== null && !keyed.has(key)) keyed.set(key, child);
    }
    const used = new Set();
    let cursor = live(parent.firstChild);
    const step = () => {
    cursor = live(cursor.nextSibling);
    };
    for (const node of desired) {
    const key = keyOf(node);
    if (key !== null) {
    const old = keyed.get(key);
    if (old && !used.has(old) && sameKind(old, node) && (shellOf(old, node) || patchable(old, node, ctx))) {
    used.add(old);
    if (old === cursor) step();
    else moveNode(parent, old, cursor, ctx);
    patchNode(old, node, ctx);
    } else {
    insertNode(parent, node, cursor, ctx);
    }
    continue;
    }
    if (!cursor) {
    insertNode(parent, node, null, ctx);
    continue;
    }
    const cursorKeyed = keyOf(cursor) !== null;
    if (!cursorKeyed && sameKind(cursor, node) && cursor.isEqualNode(node)) {
    step();
    continue;
    }
    if (!cursorKeyed && shellOf(cursor, node)) {
    const current = cursor;
    step();
    patchNode(current, node, ctx);
    continue;
    }
    const match = cursorKeyed ? null : lookAhead(cursor, node, ctx);
    if (match) {
    let stale = cursor;
    while (stale && stale !== match) {
    const next = stale.nextSibling;
    if (!skipped(stale) && keyOf(stale) === null) removeNode(stale, ctx);
    stale = next;
    }
    cursor = live(match.nextSibling);
    continue;
    }
    if (!cursorKeyed && sameKind(cursor, node)) {
    const current = cursor;
    step();
    if (patchable(current, node, ctx)) patchNode(current, node, ctx);
    else {
    insertNode(parent, node, current, ctx);
    removeNode(current, ctx);
    }
    continue;
    }
    insertNode(parent, node, cursor, ctx);
    }
    while (cursor) {
    const stale = cursor;
    cursor = live(stale.nextSibling);
    if (!used.has(stale)) removeNode(stale, ctx);
    }
    for (const old of keyed.values()) {
    if (!used.has(old) && old.parentNode === parent && !leaving(old)) removeNode(old, ctx);
    }
    }
    function morphChildren(target, nodes, options = {}) {
    const ctx = {
    replace: options.replace === true,
    options,
    active: activeIn(target),
    moved: [],
    };
    const restore = keepChoice(ctx.active);
    try {
    patchChildren(target, Array.from(nodes), ctx, true);
    } finally {
    restore?.();
    settleMoved(ctx.moved);
    }
    }
    function settleMoved(moved) {
    if (!moved.length) return;
    const view = moved[0].ownerDocument?.defaultView;
    if (typeof view?.getComputedStyle === 'function') void view.getComputedStyle(moved[0]).opacity;
    for (const node of moved) node.removeAttribute('data-ds-moving');
    }
    const START_TAG = /<!--[\s\S]*?-->|<([a-zA-Z][^\s/>]*)((?:\s+[^\s"'>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*(\/?)>/g;
    const ATTRIBUTE = /(\s+)([^\s"'>/=]+)((?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)/g;
    const RAW_TEXT = /^(?:script|style|textarea|title|xmp|iframe|noembed|noframes|noscript|plaintext)$/i;
    const STYLE_CARRIER = 'data-ds-style';
    function hideStyles(html) {
    if (!/\sstyle\s*=/i.test(html)) return html;
    let out = '';
    let last = 0;
    START_TAG.lastIndex = 0;
    for (let match = START_TAG.exec(html); match; match = START_TAG.exec(html)) {
    if (!match[1]) continue;
    const attributes = match[2].replace(ATTRIBUTE, (whole, space, name, value) => (name.toLowerCase() === 'style' ? `${space}${STYLE_CARRIER}${value}` : whole));
    out += `${html.slice(last, match.index)}<${match[1]}${attributes}${match[3] ? ' /' : ''}>`;
    last = START_TAG.lastIndex;
    if (RAW_TEXT.test(match[1])) {
    const close = html.toLowerCase().indexOf(`</${match[1].toLowerCase()}`, last);
    START_TAG.lastIndex = close < 0 ? html.length : close;
    }
    }
    return out + html.slice(last);
    }
    function carryStyles(root) {
    for (const element of Array.from(root?.querySelectorAll?.(`[${STYLE_CARRIER}]`) ?? [])) {
    const text = element.getAttribute(STYLE_CARRIER);
    element.removeAttribute(STYLE_CARRIER);
    if (element.style && typeof element.style.setProperty === 'function') element.style.cssText = text;
    else element.setAttribute('style', text);
    }
    }
    const SVG_NS = 'http://www.w3.org/2000/svg';
    const MATHML_NS = 'http://www.w3.org/1998/Math/MathML';
    function morphHtml(target, html, options = {}) {
    const template = target.ownerDocument.createElement('template');
    const foreign = target.namespaceURI === SVG_NS && target.localName !== 'foreignObject' ? 'svg'
    : target.namespaceURI === MATHML_NS ? 'math' : '';
    const markup = hideStyles(String(html));
    template.innerHTML = foreign ? `<${foreign}>${markup}</${foreign}>` : markup;
    const source = foreign ? template.content.firstChild : template.content;
    carryStyles(source);
    morphChildren(target, Array.from(source?.childNodes ?? []), options);
    }
    function own(element, kind, name) {
    if (!element || typeof element !== 'object') return;
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element?.[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const windowOf = (node) => node?.ownerDocument?.defaultView ?? globalThis;
    const idle = () => ({ update() {}, destroy() {} });
    function documentScroller(scroller) {
    const doc = scroller?.ownerDocument;
    return Boolean(doc) && (scroller === doc.scrollingElement || scroller === doc.documentElement || scroller === doc.body);
    }
    const scrollWatches = new WeakMap();
    function scrolledTarget(scroller) {
    const is = (selector) => Boolean(scroller.matches?.(selector));
    if (is('.ds-view')) return scroller.closest('.ds-app') ?? scroller;
    if (is('.ds-sheet__body')) return scroller.closest('.ds-sheet__panel') ?? scroller;
    if (is('.ds-dialog__body')) return scroller.closest('.ds-dialog__box') ?? scroller;
    return scroller;
    }
    function watchScrolled(scroller, target) {
    if (!scroller?.ownerDocument) return idle();
    const existing = scrollWatches.get(scroller);
    if (existing) return existing.handle;
    const doc = scroller.ownerDocument;
    const view = windowOf(scroller);
    const owner = target ?? scrolledTarget(scroller);
    const whole = documentScroller(scroller);
    const sentinel = doc.createElement('div');
    sentinel.setAttribute('aria-hidden', 'true');
    sentinel.setAttribute('data-ds-keep', '');
    sentinel.setAttribute('data-ds-sentinel', '');
    own(owner, 'attrs', 'data-scrolled');
    const host = () => (whole ? doc.body : scroller);
    const place = () => {
    const style = typeof view.getComputedStyle === 'function' ? view.getComputedStyle(host()) : null;
    if (whole || !style || style.position !== 'static') {
    sentinel.style.cssText = 'position: absolute; top: 0; inset-inline: 0; height: 1px; pointer-events: none';
    } else {
    sentinel.style.cssText = `height: 1px; flex-shrink: 0; margin-bottom: ${-1 - (parseFloat(style.rowGap) || 0)}px`;
    }
    if (sentinel.parentNode !== host()) host().insertBefore(sentinel, host().firstChild);
    };
    const report = (scrolled) => {
    if (scrolled === owner.hasAttribute('data-scrolled')) return;
    if (scrolled) owner.setAttribute('data-scrolled', '');
    else owner.removeAttribute('data-scrolled');
    if (!scrolled) resyncScroll(scroller);
    };
    const texts = !whole && scroller.matches?.('.ds-dialog__body');
    if (texts) own(scroller, 'attrs', 'data-ds-text');
    const markText = () => {
    if (!texts) return;
    const bare = Array.from(scroller.childNodes).some((node) => node.nodeType === 3 && /\S/.test(node.nodeValue ?? ''));
    if (bare !== scroller.hasAttribute('data-ds-text')) scroller.toggleAttribute('data-ds-text', bare);
    };
    const mutations = texts && typeof view.MutationObserver === 'function'
    ? new view.MutationObserver((records) => {
    if (records.some((record) => record.target === scroller || record.target.parentNode === scroller)) markText();
    })
    : null;
    mutations?.observe(scroller, { childList: true, characterData: true, subtree: true });
    let observer = null;
    let onScroll = null;
    const source = whole ? view : scroller;
    if (typeof view.IntersectionObserver === 'function') {
    observer = new view.IntersectionObserver((entries) => {
    const entry = entries[entries.length - 1];
    if (!entry?.boundingClientRect.height) return;
    report(!entry.isIntersecting || entry.intersectionRatio < 0.5);
    }, { root: whole ? null : scroller, threshold: [0, 0.5, 1] });
    } else {
    onScroll = () => report((whole ? view.scrollY : scroller.scrollTop) > 0);
    source.addEventListener?.('scroll', onScroll, { passive: true });
    }
    place();
    markText();
    observer?.observe(sentinel);
    const handle = {
    update() {
    if (scrollWatches.get(scroller)?.handle !== handle) return;
    const back = sentinel.parentNode !== host();
    place();
    markText();
    if (back && observer) {
    observer.unobserve(sentinel);
    observer.observe(sentinel);
    }
    onScroll?.();
    },
    destroy() {
    if (scrollWatches.get(scroller)?.handle !== handle) return;
    scrollWatches.delete(scroller);
    observer?.disconnect();
    mutations?.disconnect();
    if (onScroll) source.removeEventListener?.('scroll', onScroll);
    sentinel.remove();
    disown(owner, 'attrs', 'data-scrolled');
    owner.removeAttribute('data-scrolled');
    if (texts) {
    disown(scroller, 'attrs', 'data-ds-text');
    scroller.removeAttribute('data-ds-text');
    }
    },
    };
    scrollWatches.set(scroller, { handle });
    return handle;
    }
    const edgeWatches = new WeakMap();
    const edgeObservers = new WeakMap();
    function edgeObserver(view) {
    let entry = edgeObservers.get(view);
    if (!entry && typeof view.ResizeObserver === 'function') {
    const targets = new Map();
    const observer = new view.ResizeObserver((records) => {
    const run = new Set();
    for (const record of records) for (const update of targets.get(record.target) ?? []) run.add(update);
    for (const update of run) update();
    });
    entry = { observer, targets };
    edgeObservers.set(view, entry);
    }
    return entry ?? null;
    }
    function watchSize(view, element, update) {
    const entry = edgeObserver(view);
    if (!entry || !element) return;
    const list = entry.targets.get(element) ?? new Set();
    if (!list.size) entry.observer.observe(element);
    list.add(update);
    entry.targets.set(element, list);
    }
    function unwatchSize(view, element, update) {
    const entry = edgeObservers.get(view);
    const list = element ? entry?.targets.get(element) : null;
    if (!list) return;
    list.delete(update);
    if (!list.size) {
    entry.targets.delete(element);
    entry.observer.unobserve(element);
    }
    }
    function flag(element, name, on) {
    if (on === element.hasAttribute(name)) return;
    if (on) element.setAttribute(name, '');
    else element.removeAttribute(name);
    }
    function scrollEdges(scroller, options = {}) {
    if (!scroller?.ownerDocument) return idle();
    const axis = options.axis === 'block' ? 'block' : 'inline';
    const watches = edgeWatches.get(scroller) ?? new Map();
    edgeWatches.set(scroller, watches);
    const existing = watches.get(axis);
    if (existing) return existing.handle;
    const view = windowOf(scroller);
    const inline = axis === 'inline';
    const bar = inline ? '--ds-scrollbar-height' : '--ds-scrollbar-width';
    for (const name of ['data-ds-edges', 'data-ds-overflow-start', 'data-ds-overflow-end']) own(scroller, 'attrs', name);
    for (const name of [bar, '--ds-table-pin-width', '--ds-table-pin-last-width']) own(scroller, 'props', name);
    let frame = 0;
    let overflowed = false;
    const watched = new Set();
    const syncChildren = () => {
    const now = new Set(Array.from(scroller.children ?? []));
    for (const node of watched) {
    if (now.has(node)) continue;
    unwatchSize(view, node, schedule);
    watched.delete(node);
    }
    for (const node of now) {
    if (watched.has(node)) continue;
    watchSize(view, node, schedule);
    watched.add(node);
    }
    };
    const children = typeof view.MutationObserver === 'function'
    ? new view.MutationObserver(() => {
    syncChildren();
    schedule();
    })
    : null;
    const measure = () => {
    frame = 0;
    const size = inline ? scroller.scrollWidth - scroller.clientWidth : scroller.scrollHeight - scroller.clientHeight;
    const at = Math.abs(inline ? scroller.scrollLeft : scroller.scrollTop) || 0;
    const overflow = size > 1;
    flag(scroller, 'data-ds-overflow-start', overflow && at > 1);
    flag(scroller, 'data-ds-overflow-end', overflow && size - at > 1);
    const style = typeof view.getComputedStyle === 'function' ? view.getComputedStyle(scroller) : null;
    const borders = inline
    ? (parseFloat(style?.borderTopWidth) || 0) + (parseFloat(style?.borderBottomWidth) || 0)
    : (parseFloat(style?.borderLeftWidth) || 0) + (parseFloat(style?.borderRightWidth) || 0);
    flag(scroller, 'data-ds-edges', true);
    const room = Math.max(0, (inline ? scroller.offsetHeight - scroller.clientHeight : scroller.offsetWidth - scroller.clientWidth) - borders);
    scroller.style.setProperty(bar, `${Math.round(room)}px`);
    const pinned = scroller.querySelector?.('.ds-table--pin-first') ?? null;
    const first = pinned ? pinned.rows?.[0]?.cells?.[0] ?? pinned.querySelector('th, td') : null;
    if (first) scroller.style.setProperty('--ds-table-pin-width', `${Math.round(first.getBoundingClientRect().width * 100) / 100}px`);
    else scroller.style.removeProperty('--ds-table-pin-width');
    const pinnedEnd = scroller.querySelector?.('.ds-table--pin-last') ?? null;
    const row = pinnedEnd?.rows?.[0]?.cells;
    const last = row?.length ? row[row.length - 1] : pinnedEnd ? [...pinnedEnd.querySelectorAll('th, td')].at(-1) : null;
    if (last) scroller.style.setProperty('--ds-table-pin-last-width', `${Math.round(last.getBoundingClientRect().width * 100) / 100}px`);
    else scroller.style.removeProperty('--ds-table-pin-last-width');
    if (overflowed && !overflow) resyncScroll(scroller);
    overflowed = overflow;
    if (!children) syncChildren();
    };
    const schedule = () => {
    if (frame) return;
    if (typeof view.requestAnimationFrame !== 'function') {
    measure();
    return;
    }
    frame = view.requestAnimationFrame(measure);
    };
    scroller.addEventListener('scroll', schedule, { passive: true });
    watchSize(view, scroller, schedule);
    syncChildren();
    children?.observe(scroller, { childList: true });
    measure();
    const handle = {
    update() {
    if (watches.get(axis)?.handle === handle) measure();
    },
    destroy() {
    if (watches.get(axis)?.handle !== handle) return;
    watches.delete(axis);
    if (frame) view.cancelAnimationFrame?.(frame);
    scroller.removeEventListener('scroll', schedule);
    unwatchSize(view, scroller, schedule);
    children?.disconnect();
    for (const node of watched) unwatchSize(view, node, schedule);
    watched.clear();
    for (const name of ['data-ds-edges', 'data-ds-overflow-start', 'data-ds-overflow-end']) {
    disown(scroller, 'attrs', name);
    if (!watches.size) scroller.removeAttribute(name);
    }
    disown(scroller, 'props', bar);
    disown(scroller, 'props', '--ds-table-pin-width');
    scroller.style.removeProperty(bar);
    if (!watches.size) scroller.style.removeProperty('--ds-table-pin-width');
    if (scroller.getAttribute('style')?.trim() === '') scroller.removeAttribute('style');
    },
    };
    watches.set(axis, { handle, update: measure });
    return handle;
    }
    let warnedText = false;
    function progress(el, share, options = {}) {
    if (!el) return el;
    const list = el.classList;
    const meter = list.contains('ds-meter');
    const spinner = list.contains('ds-spinner');
    const aria = spinner ? el : meter ? (el.getAttribute('role') === 'meter' ? el : null) : el.querySelector?.('.ds-progress__track[role="progressbar"], [role="progressbar"]') ?? null;
    const text = options.valueText;
    if (share === null || share === undefined || !Number.isFinite(Number(share))) {
    if (meter) return el;
    if (spinner) list.remove('ds-spinner--determinate');
    else {
    list.toggle('ds-progress--indeterminate', !options.waiting);
    list.toggle('ds-progress--waiting', Boolean(options.waiting));
    }
    if (aria) {
    aria.removeAttribute('aria-valuenow');
    if (text) aria.setAttribute('aria-valuetext', String(text));
    else {
    aria.removeAttribute('aria-valuetext');
    if (!warnedText) {
    warnedText = true;
    globalThis.console?.warn?.('DS.dom.progress: an unknown share needs a valueText ("Preparing", "Waiting") for assistive tech');
    }
    }
    }
    return el;
    }
    const value = Math.round(Math.min(1, Math.max(0, Number(share))) * 1000) / 1000;
    if (spinner) list.add('ds-spinner--determinate');
    else if (!meter) list.remove('ds-progress--indeterminate', 'ds-progress--waiting');
    ( (el)).style.setProperty(meter ? '--ds-share' : '--ds-progress', String(value));
    if (aria) {
    aria.setAttribute('aria-valuenow', String(Math.round(value * 100)));
    if (text) aria.setAttribute('aria-valuetext', String(text));
    else aria.removeAttribute('aria-valuetext');
    }
    return el;
    }
    const rovers = new WeakMap();
    const ROVING_ITEMS = '[role="tab"], [role="radio"], [role="option"]';
    function roving(container, options = {}) {
    if (!container?.ownerDocument) return { destroy() {} };
    const existing = rovers.get(container);
    if (existing) return existing.handle;
    const selector = options.items ?? ROVING_ITEMS;
    const orientation = options.orientation ?? 'horizontal';
    const wrap = options.wrap !== false;
    const activate = options.activate === undefined ? 'focus' : options.activate;
    const view = windowOf(container);
    const items = () => Array.from(container.querySelectorAll(selector)).filter((item) => !item.matches?.(':disabled') && !item.hidden && !item.closest?.('[hidden], [inert]'));
    const chosen = (item) => ['aria-selected', 'aria-checked', 'aria-pressed'].some((name) => item.getAttribute(name) === 'true') || (item.hasAttribute('aria-current') && item.getAttribute('aria-current') !== 'false');
    let last = null;
    const stop = (current) => {
    last = current;
    for (const item of items()) {
    const value = item === current ? '0' : '-1';
    if (item.getAttribute('tabindex') !== value) item.setAttribute('tabindex', value);
    }
    };
    const restop = () => {
    const all = items();
    const active = container.ownerDocument.activeElement;
    stop(all.find((item) => item === active || item.contains?.(active))
    ?? all.find((item) => item.getAttribute('tabindex') === '0')
    ?? all.find(chosen)
    ?? (all.includes(last) ? last : null)
    ?? all[0] ?? null);
    };
    restop();
    const changes = typeof view.MutationObserver === 'function' ? new view.MutationObserver(restop) : null;
    changes?.observe(container, { childList: true, subtree: true, attributes: true, attributeFilter: ['tabindex', 'aria-selected', 'aria-checked', 'aria-pressed', 'aria-current', 'disabled', 'hidden'] });
    const run = (item) => {
    if (item.getAttribute('aria-disabled') === 'true') return;
    if (typeof options.onActivate === 'function') options.onActivate(item);
    else item.click?.();
    };
    const nearest = (list, from, down) => {
    const box = from.getBoundingClientRect();
    const rows = list.filter((item) => item !== from).map((item) => ({ item, box: item.getBoundingClientRect() }))
    .filter(({ box: other }) => (down ? other.top >= box.bottom - 1 : other.bottom <= box.top + 1));
    if (!rows.length) return null;
    const row = down ? Math.min(...rows.map(({ box: other }) => other.top)) : Math.max(...rows.map(({ box: other }) => other.top));
    const centre = (rect) => rect.left + rect.width / 2;
    return rows.filter(({ box: other }) => Math.abs(other.top - row) < 1)
    .sort((a, b) => Math.abs(centre(a.box) - centre(box)) - Math.abs(centre(b.box) - centre(box)))[0].item;
    };
    const onKey = ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    const list = items();
    const current = list.find((item) => item === event.target || item.contains(event.target));
    if (!current) return;
    if (event.key === 'Enter' || event.key === ' ') {
    if (activate === 'manual' && current.tagName !== 'BUTTON' && current.tagName !== 'A' && current.tagName !== 'INPUT') {
    event.preventDefault();
    run(current);
    }
    return;
    }
    const rtl = typeof view.getComputedStyle === 'function' && view.getComputedStyle(container).direction === 'rtl';
    const index = list.indexOf(current);
    const step = (by) => {
    const at = index + by;
    if (at >= 0 && at < list.length) return list[at];
    return wrap ? list[(at + list.length) % list.length] : null;
    };
    const across = orientation !== 'vertical';
    const along = orientation !== 'horizontal';
    let next;
    if (event.key === 'Home') next = list[0];
    else if (event.key === 'End') next = list[list.length - 1];
    else if (across && event.key === 'ArrowRight') next = step(rtl ? -1 : 1);
    else if (across && event.key === 'ArrowLeft') next = step(rtl ? 1 : -1);
    else if (along && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
    const down = event.key === 'ArrowDown';
    next = orientation === 'both' ? nearest(list, current, down) ?? (wrap ? (down ? list[0] : list[list.length - 1]) : null) : step(down ? 1 : -1);
    } else return;
    event.preventDefault();
    if (!next || next === current) return;
    stop(next);
    next.focus();
    if (activate === 'focus') run(next);
    };
    const onFocus = ( event) => {
    const item = items().find((each) => each === event.target);
    if (item) stop(item);
    };
    container.addEventListener('keydown', onKey);
    container.addEventListener('focusin', onFocus);
    const handle = {
    destroy() {
    if (rovers.get(container)?.handle !== handle) return;
    rovers.delete(container);
    changes?.disconnect();
    container.removeEventListener('keydown', onKey);
    container.removeEventListener('focusin', onFocus);
    },
    };
    rovers.set(container, { handle });
    return handle;
    }
    const KEYBOARD_MIN_PX = 150;
    const viewportFollowers = new WeakMap();
    function frameDocument(frame) {
    try {
    return frame.contentDocument || null;
    } catch {
    return null;
    }
    }
    function typesText(node) {
    if (!node) return false;
    if (node.isContentEditable) return true;
    if (node.tagName === 'TEXTAREA') return !node.readOnly;
    if (node.tagName !== 'INPUT') return false;
    return !node.readOnly && !/^(button|checkbox|color|file|hidden|image|radio|range|reset|submit)$/i.test(node.type);
    }
    function editing(doc) {
    let active = doc.activeElement;
    for (;;) {
    while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement;
    if (!/^i?frame$/i.test(active?.tagName || '')) break;
    const inner = frameDocument(active);
    if (!inner) return false;
    active = inner.activeElement;
    doc = inner;
    }
    if (!active || active === doc.body) return false;
    return typesText(active);
    }
    function bottomInset(doc, probes) {
    if (!probes.probe) {
    if (!doc.body || typeof doc.createElement !== 'function') return 0;
    const probe = doc.createElement('div');
    probe.setAttribute('aria-hidden', 'true');
    probe.setAttribute('data-ds-keep', '');
    probe.style.cssText = 'position:fixed;left:0;bottom:0;width:0;height:env(safe-area-inset-bottom, 0px);margin:0;border:0;padding:0;visibility:hidden;pointer-events:none';
    probes.probe = probe;
    if (typeof probes.view.ResizeObserver === 'function') {
    probes.sizes = new probes.view.ResizeObserver(probes.changed);
    probes.sizes.observe(probe);
    }
    }
    if (!probes.probe.parentNode || !probes.probe.isConnected) doc.body?.append(probes.probe);
    return probes.probe.parentNode ? probes.probe.getBoundingClientRect().height || 0 : 0;
    }
    function panToTop(root) {
    if (typeof root.scrollIntoView !== 'function') return;
    try {
    root.scrollIntoView({ block: 'start', inline: 'nearest', behavior: 'instant' });
    } catch {
    root.scrollIntoView(true);
    }
    }
    function followViewport(doc = globalThis.document) {
    const view = doc?.defaultView;
    const viewport = view?.visualViewport;
    if (!viewport || !doc.documentElement) return { destroy() {} };
    const existing = viewportFollowers.get(doc);
    if (existing) return existing.handle;
    const root = doc.documentElement;
    const tallest = new Map();
    own(root, 'attrs', 'data-ds-keyboard');
    for (const name of ['--ds-viewport-height', '--ds-keyboard-inset']) own(root, 'props', name);
    let frame = 0;
    let openInset = 0;
    const probes = { view, probe: null, sizes: null, changed: () => schedule() };
    const update = () => {
    frame = 0;
    const scale = viewport.scale || 1;
    const width = Math.round(viewport.width * scale);
    const height = viewport.height;
    const top = Math.max(height, tallest.get(width) ?? 0);
    tallest.set(width, top);
    const layout = view.innerHeight || height;
    const short = top - height > KEYBOARD_MIN_PX;
    const typing = editing(doc);
    const inset = bottomInset(doc, probes);
    if (scale === 1 && layout - height < 2 && !typing) openInset = inset;
    const lost = short && scale === 1 && inset < 0.5 ? openInset : 0;
    root.style.setProperty('--ds-viewport-height', `${Math.round(height - lost)}px`);
    const keyboard = short && typing;
    const covered = Math.max(0, Math.round(layout - height - viewport.offsetTop + lost));
    root.style.setProperty('--ds-keyboard-inset', `${keyboard ? covered : 0}px`);
    if (keyboard !== root.hasAttribute('data-ds-keyboard')) {
    if (keyboard) root.setAttribute('data-ds-keyboard', '');
    else root.removeAttribute('data-ds-keyboard');
    }
    if (keyboard && scale === 1) {
    if ((view.scrollY || 0) > 0 && viewport.offsetTop > 0) view.scrollTo(view.scrollX || 0, 0);
    else if (viewport.offsetTop >= 1) panToTop(root);
    }
    };
    const schedule = () => {
    if (frame) return;
    frame = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(update) : (update(), 0);
    };
    viewport.addEventListener('resize', schedule);
    viewport.addEventListener('scroll', schedule);
    doc.addEventListener('focusin', schedule);
    doc.addEventListener('focusout', schedule);
    update();
    const handle = {
    destroy() {
    if (viewportFollowers.get(doc)?.handle !== handle) return;
    viewportFollowers.delete(doc);
    if (frame) view.cancelAnimationFrame?.(frame);
    viewport.removeEventListener('resize', schedule);
    viewport.removeEventListener('scroll', schedule);
    doc.removeEventListener('focusin', schedule);
    doc.removeEventListener('focusout', schedule);
    probes.sizes?.disconnect();
    probes.probe?.remove();
    disown(root, 'attrs', 'data-ds-keyboard');
    for (const name of ['--ds-viewport-height', '--ds-keyboard-inset']) {
    disown(root, 'props', name);
    root.style.removeProperty(name);
    }
    root.removeAttribute('data-ds-keyboard');
    if (root.getAttribute('style')?.trim() === '') root.removeAttribute('style');
    },
    };
    viewportFollowers.set(doc, { handle });
    return handle;
    }
    const endFollowers = new WeakMap();
    function followEnd(scroller, options = {}) {
    if (!scroller?.ownerDocument) return { stick() {}, destroy() {} };
    const existing = endFollowers.get(scroller);
    if (existing) return existing.handle;
    const view = windowOf(scroller);
    const pill = options.pill ?? null;
    const threshold = Math.max(0, Number(options.threshold ?? 1));
    const gap = () => scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop;
    let stuck = gap() <= threshold;
    let last = scroller.scrollTop;
    const show = () => {
    if (pill && pill.hidden !== stuck) pill.hidden = stuck;
    };
    const toEnd = (smooth) => {
    const end = scroller.scrollHeight - scroller.clientHeight;
    if (smooth && typeof scroller.scrollTo === 'function') {
    last = scroller.scrollTop;
    scroller.scrollTo({ top: end, behavior: 'smooth' });
    } else {
    scroller.scrollTop = end;
    last = end;
    }
    };
    const onScroll = () => {
    const top = scroller.scrollTop;
    if (gap() <= threshold) stuck = true;
    else if (top < last - 0.5) stuck = false;
    last = top;
    show();
    };
    const grew = () => {
    if (stuck && gap() > threshold) toEnd(false);
    last = scroller.scrollTop;
    show();
    };
    const sizes = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(grew) : null;
    const children = typeof view.MutationObserver === 'function' ? new view.MutationObserver((records) => {
    for (const record of records) {
    for (const node of Array.from(record.removedNodes ?? [])) if (node.nodeType === 1 && node.parentNode !== scroller) sizes?.unobserve(node);
    for (const node of Array.from(record.addedNodes ?? [])) if (node.nodeType === 1 && node.parentNode === scroller) sizes?.observe(node);
    }
    grew();
    }) : null;
    for (const child of Array.from(scroller.children ?? [])) sizes?.observe(child);
    sizes?.observe(scroller);
    children?.observe(scroller, { childList: true });
    scroller.addEventListener('scroll', onScroll, { passive: true });
    const stick = () => {
    stuck = true;
    toEnd(gap() <= scroller.clientHeight);
    show();
    };
    const onPill = () => stick();
    pill?.addEventListener('click', onPill);
    if (pill) own(pill, 'attrs', 'hidden');
    show();
    const handle = {
    stick,
    destroy() {
    if (endFollowers.get(scroller)?.handle !== handle) return;
    endFollowers.delete(scroller);
    sizes?.disconnect();
    children?.disconnect();
    scroller.removeEventListener('scroll', onScroll);
    pill?.removeEventListener('click', onPill);
    if (pill) disown(pill, 'attrs', 'hidden');
    },
    };
    endFollowers.set(scroller, { handle });
    return handle;
    }
    const autosized = new WeakMap();
    function autosize(textarea) {
    if (!textarea?.ownerDocument) return idle();
    const existing = autosized.get(textarea);
    if (existing) return existing.handle;
    const view = windowOf(textarea);
    const style = () => (typeof view.getComputedStyle === 'function' ? view.getComputedStyle(textarea) : null);
    const native = () => style()?.getPropertyValue?.('field-sizing') === 'content';
    let owned = false;
    const release = () => {
    if (owned) {
    owned = false;
    disown(textarea, 'props', 'height');
    disown(textarea, 'props', 'overflow-y');
    }
    textarea.style.removeProperty('height');
    textarea.style.removeProperty('overflow-y');
    if (textarea.getAttribute('style')?.trim() === '') textarea.removeAttribute('style');
    };
    const update = () => {
    if (textarea.isConnected === false || typeof textarea.getClientRects === 'function' && !textarea.getClientRects().length) return;
    if (native()) {
    if (owned) release();
    return;
    }
    if (!owned) {
    owned = true;
    own(textarea, 'props', 'height');
    own(textarea, 'props', 'overflow-y');
    }
    const computed = style();
    const borders = computed?.boxSizing === 'border-box' ? (parseFloat(computed.borderTopWidth) || 0) + (parseFloat(computed.borderBottomWidth) || 0) : 0;
    const max = parseFloat(computed?.maxHeight);
    textarea.style.height = 'auto';
    const wanted = textarea.scrollHeight + borders;
    const capped = Number.isFinite(max) && max > 0 ? Math.min(wanted, max) : wanted;
    textarea.style.height = `${Math.ceil(capped)}px`;
    textarea.style.overflowY = Number.isFinite(max) && wanted > max ? 'auto' : 'hidden';
    };
    let width = -1;
    let frame = 0;
    const sizes = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver((entries) => {
    const inline = entries[entries.length - 1]?.contentRect?.width ?? 0;
    if (inline === width) return;
    width = inline;
    if (inline > 0 && !frame) frame = view.requestAnimationFrame(() => { frame = 0; update(); });
    }) : null;
    sizes?.observe(textarea);
    textarea.addEventListener('input', update);
    update();
    const handle = {
    update,
    destroy() {
    if (autosized.get(textarea)?.handle !== handle) return;
    autosized.delete(textarea);
    sizes?.disconnect();
    if (frame) view.cancelAnimationFrame(frame);
    textarea.removeEventListener('input', update);
    release();
    },
    };
    autosized.set(textarea, { handle });
    return handle;
    }
    const dropzones = new WeakMap();
    const carriesFiles = (event) => Array.from(event.dataTransfer?.types ?? []).includes('Files');
    function accepts(file, accept) {
    const rules = String(accept ?? '').split(',').map((rule) => rule.trim().toLowerCase()).filter(Boolean);
    if (!rules.length) return true;
    const name = String(file?.name ?? '').toLowerCase();
    const type = String(file?.type ?? '').toLowerCase();
    return rules.some((rule) => {
    if (rule.startsWith('.')) return name.endsWith(rule);
    if (rule.endsWith('/*')) return type.startsWith(rule.slice(0, -1));
    return type === rule;
    });
    }
    function dropzone(zone, options) {
    if (!zone?.ownerDocument) return { destroy() {} };
    const existing = dropzones.get(zone);
    if (existing) return existing.handle;
    const doc = zone.ownerDocument;
    const target = options?.target ?? zone;
    const input = options?.input !== undefined
    ? options.input
    : zone.querySelector?.('.ds-dropzone__input') ?? zone.querySelector?.('input[type="file"]') ?? null;
    const accept = () => options?.accept ?? input?.accept ?? '';
    const multiple = () => options?.multiple ?? Boolean(input?.multiple);
    const blocked = () => Boolean(input?.disabled);
    let depth = 0;
    own(zone, 'attrs', 'data-ds-dragover');
    const over = (on) => {
    if (on === zone.hasAttribute('data-ds-dragover')) return;
    if (on) zone.setAttribute('data-ds-dragover', '');
    else zone.removeAttribute('data-ds-dragover');
    };
    const hand = (list, event) => {
    const files = Array.from(list ?? []).filter((file) => accepts(file, accept()));
    const taken = multiple() ? files : files.slice(0, 1);
    if (taken.length) options?.onFiles?.(taken, event);
    return taken.length > 0;
    };
    const onEnter = (event) => {
    if (!carriesFiles(event) || blocked()) return;
    event.preventDefault();
    depth += 1;
    over(true);
    };
    const onOver = (event) => {
    if (!carriesFiles(event) || blocked()) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
    };
    const onLeave = (event) => {
    if (!carriesFiles(event)) return;
    depth = Math.max(0, depth - 1);
    if (!depth) over(false);
    };
    const onDrop = (event) => {
    if (!carriesFiles(event)) return;
    event.preventDefault();
    depth = 0;
    over(false);
    if (!blocked()) hand(event.dataTransfer?.files, event);
    };
    const onChange = (event) => {
    hand(input.files, event);
    input.value = '';
    };
    const onPaste = (event) => {
    if (blocked() || !target.contains?.(doc.activeElement) && doc.activeElement !== target) return;
    const files = event.clipboardData?.files;
    if (!files?.length) return;
    if (hand(files, event)) event.preventDefault();
    };
    target.addEventListener('dragenter', onEnter);
    target.addEventListener('dragover', onOver);
    target.addEventListener('dragleave', onLeave);
    target.addEventListener('drop', onDrop);
    input?.addEventListener('change', onChange);
    if (options?.paste) doc.addEventListener('paste', onPaste);
    const handle = {
    destroy() {
    if (dropzones.get(zone)?.handle !== handle) return;
    dropzones.delete(zone);
    target.removeEventListener('dragenter', onEnter);
    target.removeEventListener('dragover', onOver);
    target.removeEventListener('dragleave', onLeave);
    target.removeEventListener('drop', onDrop);
    input?.removeEventListener('change', onChange);
    doc.removeEventListener('paste', onPaste);
    disown(zone, 'attrs', 'data-ds-dragover');
    zone.removeAttribute('data-ds-dragover');
    },
    };
    dropzones.set(zone, { handle });
    return handle;
    }

    return Object.freeze({
      morphChildren: morphChildren,
      morphHtml: morphHtml,
      own: own,
      disown: disown,
      watchScrolled: watchScrolled,
      scrollEdges: scrollEdges,
      progress: progress,
      roving: roving,
      typesText: typesText,
      followViewport: followViewport,
      followEnd: followEnd,
      autosize: autosize,
      dropzone: dropzone
    });
  });

  add('ripple', ['RIPPLE_HOSTS', 'attach'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, runtime = DS.core.runtime;
    const flip = DS.motion.flip, guard = DS.motion.guard, motionDuration = DS.motion.motionDuration, motionEasing = DS.motion.motionEasing, settled = DS.motion.settled, travelDuration = DS.motion.travelDuration;
    const RIPPLE_HOSTS = [
    '.ds-pressable',
    '.ds-btn',
    '.ds-disclosure > summary',
    '.ds-errors summary',
    ':is(button, a).ds-columns__col',
    '.ds-chart__key',
    ':is(button, a).ds-tile',
    '.ds-list--interactive > .ds-list__item',
    ':is(a, button).ds-kv-list__row',
    '.ds-list[data-ds-list-select] > .ds-list__item:not(:has(.ds-list__action)):not(:has(> .ds-list__check:disabled))',
    '.ds-card--interactive',
    '.ds-menu__item',
    '.ds-chip',
    '.ds-table__sort',
    '.ds-table--interactive tbody tr:not(.ds-table__detail):has(.ds-table__row-link)',
    '.ds-radio',
    '.ds-check-card:not(:has(input:is(:disabled,[aria-disabled="true"])))',
    '.ds-dropzone',
    '.ds-combo__toggle',
    '.ds-combo__clear',
    '.ds-segmented__option',
    '.ds-drawer__item',
    '.ds-drawer__toggle',
    '.ds-tabs__tab',
    '.ds-steps__link',
    '.ds-calendar__day',
    '.ds-picker__preset',
    '.ds-tree__item:not([aria-disabled="true"]) > .ds-tree__row',
    '.ds-toc__link',
    '[data-ds-ripple]',
    ].join(', ');
    const HOST_LINK = [
    '.ds-table__row-link',
    '.ds-card__action',
    '.ds-list__action',
    ].join(', ');
    const OWNS_INPUT = [
    '.ds-chip',
    '.ds-radio',
    '.ds-check-card',
    ].join(', ');
    const OWN_PRESS = 'input[type="checkbox"], input[type="radio"], .ds-switch';
    const STRETCHED = [
    '.ds-card--interactive',
    '.ds-table--interactive tbody tr:has(.ds-table__row-link)',
    '.ds-list--interactive > .ds-list__item',
    ].join(', ');
    const CONTROLS = 'a[href], button, input, select, textarea, label, summary, [tabindex]';
    const presses = new WeakMap();
    const pressing = new Set();
    const attached = runtime.state('ripple', () => new WeakMap());
    const now = () => (typeof performance !== 'undefined' ? performance.now() : Date.now());
    function hostOf(target, root) {
    const element = target?.nodeType === 1 ? target : target?.parentElement;
    const host = element?.closest?.(RIPPLE_HOSTS);
    if (!host || !root.contains(host)) return null;
    if (host.closest('[data-ds-ripple="off"]')) return null;
    return host;
    }
    function surfaceOf(host) {
    return host.classList.contains('ds-chip') ? host.querySelector('.ds-chip__body') : host;
    }
    function holderOf(surface) {
    if (surface.tagName !== 'TR') return surface;
    return (pinnedCell(surface) ? surface.cells[1] : null) ?? surface.cells[0] ?? null;
    }
    const PINNED_ROW = '.ds-table--pin-first.ds-table--interactive tbody tr:not(.ds-table__detail)';
    function stuck(row, cell, pin) {
    if (!cell || row.cells.length < 2 || !row.closest(pin)) return null;
    return row.ownerDocument.defaultView?.getComputedStyle(cell).position === 'sticky' ? cell : null;
    }
    const pinnedCell = (row) => stuck(row, row.cells?.[0], '.ds-table--pin-first');
    const pinnedLastCell = (row) => stuck(row, row.cells?.[row.cells.length - 1], '.ds-table--pin-last');
    function inPinnedRoom(row, element) {
    const cell = pinnedLastCell(row);
    if (!cell?.contains(element)) return false;
    const control = element.closest(CONTROLS);
    return !(control && cell.contains(control));
    }
    function rippleSpan(doc, x, y, radius) {
    const span = doc.createElement('span');
    span.className = 'ds-press-ripple';
    span.setAttribute('aria-hidden', 'true');
    span.setAttribute('data-ds-keep', '');
    span.style.setProperty('--ds-ripple-x', `${Math.round(x * 100) / 100}px`);
    span.style.setProperty('--ds-ripple-y', `${Math.round(y * 100) / 100}px`);
    span.style.setProperty('--ds-ripple-r', `${Math.round(radius * 100) / 100}px`);
    return span;
    }
    function pinnedPart(row, cell, rect, left, top, radius) {
    const box = cell.getBoundingClientRect();
    const part = rippleSpan(row.ownerDocument, left + rect.left - box.left, top + rect.top - box.top, radius);
    if (cell === row.cells[0]) cell.append(part);
    else cell.prepend(part);
    return part;
    }
    function forwardRowClick(event, root, ownPrevent = false) {
    if ((event.defaultPrevented && !ownPrevent) || (event.button !== 0 && event.button !== 1)) return false;
    const element = event.target?.nodeType === 1 ? event.target : event.target?.parentElement;
    const row = element?.closest?.(PINNED_ROW);
    if (!row || !root.contains(row)) return false;
    const control = element.closest(CONTROLS);
    if (control && row.contains(control)) return false;
    if (inPinnedRoom(row, element)) return false;
    const link = row.querySelector('.ds-table__row-link');
    if (!link || link.matches(':disabled') || link.getAttribute('aria-disabled') === 'true') return false;
    const view = row.ownerDocument.defaultView;
    const selection = view?.getSelection?.();
    if (selection && !selection.isCollapsed && !(event.ctrlKey || event.metaKey || event.button === 1)) return false;
    const away = event.button === 1 || event.ctrlKey || event.metaKey || event.shiftKey;
    if (away) {
    if (!link.matches('a[href]')) {
    if (event.button !== 0) return false;
    link.click();
    return true;
    }
    event.preventDefault();
    view?.open( (link).href, '_blank', 'noopener');
    return true;
    }
    link.click();
    return true;
    }
    const blocked = (element) => Boolean(element && (element.matches(':disabled') || element.getAttribute('aria-disabled') === 'true'));
    function inactive(host) {
    if (blocked(host) || host.getAttribute('aria-busy') === 'true') return true;
    if (host.classList.contains('ds-btn--done') || host.classList.contains('ds-radio--disabled')) return true;
    if (host.matches(OWNS_INPUT) && blocked(host.querySelector('input'))) return true;
    if (host.matches(STRETCHED)) {
    const link = host.querySelector(HOST_LINK);
    if (link && blocked(link)) return true;
    }
    return false;
    }
    function pressesOther(host, target) {
    if (!host.matches(STRETCHED)) return false;
    const element = target?.nodeType === 1 ? target : target?.parentElement;
    const control = element?.closest?.(CONTROLS);
    return Boolean(control && control !== host && host.contains(control) && !control.matches(HOST_LINK));
    }
    function fade(span) {
    span.classList.add('ds-press-ripple--out');
    settled(span, { event: 'transitionend', property: 'opacity' }).then(() => span.remove());
    }
    function release(host, press) {
    if (press.released) return;
    press.released = true;
    press.stop?.();
    pressing.delete(host);
    const wait = Math.max(0, motionDuration(host, '--ds-dur-instant') - (now() - press.at));
    setTimeout(() => {
    if (presses.get(host) === press) {
    presses.delete(host);
    host.classList.remove('ds-pressed');
    }
    fade(press.span);
    press.twins?.forEach(fade);
    }, wait);
    }
    function start(host, x, y) {
    const surface = surfaceOf(host);
    if (!surface) return null;
    const view = surface.ownerDocument?.defaultView;
    const style = view?.getComputedStyle(surface);
    if (style?.position === 'static' && style.transform === 'none') return null;
    const previous = presses.get(host);
    if (previous && !previous.released) {
    previous.released = true;
    previous.stop?.();
    fade(previous.span);
    previous.twins?.forEach(fade);
    }
    const holder = holderOf(surface);
    if (!holder) return null;
    const rect = surface.getBoundingClientRect();
    const row = surface !== holder;
    const scaleX = !row && surface.offsetWidth ? rect.width / surface.offsetWidth : 1;
    const scaleY = !row && surface.offsetHeight ? rect.height / surface.offsetHeight : 1;
    const width = row ? rect.width : surface.clientWidth;
    const height = row ? rect.height : surface.clientHeight;
    const left = x === undefined ? width / 2 : (x - rect.left) / (scaleX || 1) - (row ? 0 : surface.clientLeft);
    const top = y === undefined ? height / 2 : (y - rect.top) / (scaleY || 1) - (row ? 0 : surface.clientTop);
    const radius = Math.hypot(Math.max(left, width - left), Math.max(top, height - top));
    const first = row ? pinnedCell(surface) : null;
    const last = row ? pinnedLastCell(surface) : null;
    let span;
    if (holder === last) {
    span = pinnedPart(surface, last, rect, left, top, radius);
    } else {
    span = rippleSpan(surface.ownerDocument, left, top, radius);
    if (row) span.style.height = `${Math.round(height * 100) / 100}px`;
    holder.append(span);
    }
    const twins = [first, holder === last ? null : last].filter(Boolean)
    .map((cell) => pinnedPart(surface, cell, rect, left, top, radius));
    host.classList.add('ds-pressed');
    const press = { span, at: now(), released: false };
    if (twins.length) press.twins = twins;
    presses.set(host, press);
    pressing.add(host);
    return press;
    }
    function end(host) {
    const press = presses.get(host);
    if (press) release(host, press);
    }
    const slotGlides = new WeakMap();
    function timeOf(text) {
    const match = /^\s*(-?\d*\.?\d+)(m?s)\s*$/.exec(String(text ?? '').split(',')[0]);
    return match ? Number(match[1]) * (match[2] === 's' ? 1000 : 1) : 0;
    }
    function easingOf(text) {
    const match = /^\s*([\w-]+\([^)]*\)|[\w-]+)/.exec(String(text ?? ''));
    return match ? match[1] : 'ease';
    }
    function glideSlot(body, from, to, timing) {
    if (Math.abs(from - to) < 0.5 || !(timing.duration > 0) || typeof body.animate !== 'function') return;
    try {
    const animation = body.animate([{ width: `${from}px` }, { width: `${to}px` }], {
    pseudoElement: '::after',
    duration: timing.duration,
    delay: timing.delay,
    easing: timing.easing,
    fill: 'backwards',
    });
    animation.id = 'ds-chip-slot';
    slotGlides.set(body, animation);
    const done = () => {
    if (slotGlides.get(body) === animation) slotGlides.delete(body);
    };
    animation.finished.then(done, done);
    } catch {
    }
    }
    const edgeGlides = new WeakMap();
    function insetRight(clip) {
    const match = /^inset\(([^)]*)\)/.exec(String(clip ?? '').trim());
    if (!match) return 0;
    const sides = match[1].split(/\s+round\s+/)[0].trim().split(/\s+/).map((value) => parseFloat(value) || 0);
    return sides.length > 1 ? sides[1] : sides[0] ?? 0;
    }
    function translateX(value) {
    return parseFloat(String(value ?? '').split(/\s+/)[0]) || 0;
    }
    function labelsOf(body) {
    return Array.from(body.children).filter((child) => !child.matches('.ds-chip__check, .ds-press-ripple'));
    }
    function edgeState(body) {
    const view = body.ownerDocument.defaultView;
    const label = labelsOf(body)[0];
    return { clip: insetRight(view.getComputedStyle(body).clipPath), shift: label ? translateX(view.getComputedStyle(label).translate) : 0 };
    }
    function stopEdge(body) {
    for (const animation of edgeGlides.get(body) ?? []) animation.cancel();
    edgeGlides.delete(body);
    }
    function glideEdge(body, grow, shift, timing) {
    if (typeof body.animate !== 'function' || (Math.abs(grow) < 0.5 && Math.abs(shift) < 0.5)) return;
    const view = body.ownerDocument.defaultView;
    const style = view.getComputedStyle(body);
    const radius = parseFloat(style.borderTopRightRadius) || 0;
    const ring = style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) > 0
    ? Math.max(0, parseFloat(style.outlineOffset) || 0) + parseFloat(style.outlineWidth) + 1
    : 0;
    const clip = (right) => `inset(${-ring}px ${Math.round(right * 100) / 100}px ${-ring}px ${-ring}px round ${radius + ring}px)`;
    const options = { duration: timing.duration, easing: timing.easing };
    const running = [];
    try {
    running.push(body.animate([{ clipPath: clip(Math.max(0, grow)) }, { clipPath: clip(0) }], options));
    if (Math.abs(shift) >= 0.5) {
    for (const label of labelsOf(body)) running.push(label.animate([{ translate: `${Math.round(shift * 100) / 100}px 0px` }, { translate: '0px 0px' }], options));
    }
    } catch {
    for (const animation of running) animation.cancel();
    return;
    }
    for (const animation of running) animation.id = 'ds-chip-edge';
    edgeGlides.set(body, running);
    const done = () => {
    if (edgeGlides.get(body) === running) edgeGlides.delete(body);
    };
    Promise.allSettled(running.map((animation) => animation.finished)).then(done);
    }
    function flipTiming(element) {
    const animation = element?.getAnimations?.().find((item) => item.id === 'ds-flip' && item.playState !== 'finished' && item.playState !== 'idle');
    const timing = animation?.effect?.getTiming?.();
    return timing && Number(timing.duration) > 0 ? { duration: Number(timing.duration), easing: timing.easing || 'linear' } : null;
    }
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    if (!element || typeof element !== 'object') return;
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element?.[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const backedRows = new WeakSet();
    function paints(colour) {
    if (!colour || colour === 'transparent') return false;
    const inner = /\(([^)]*)\)/.exec(colour)?.[1] ?? '';
    const slash = inner.split('/');
    if (slash.length > 1) return parseFloat(slash[1]) > 0;
    const parts = inner.split(',');
    return parts.length === 4 ? parseFloat(parts[3]) > 0 : true;
    }
    function backdropOf(element) {
    const view = element.ownerDocument.defaultView;
    for (let node = element; node && node.nodeType === 1; node = node.parentElement) {
    const colour = view.getComputedStyle(node).backgroundColor;
    if (paints(colour)) return colour;
    }
    return '';
    }
    function backChips(row) {
    if (!backedRows.has(row)) {
    backedRows.add(row);
    own(row, 'attrs', 'data-ds-glide');
    own(row, 'props', '--ds-chips-backdrop');
    }
    const backdrop = backdropOf(row);
    if (backdrop) row.style.setProperty('--ds-chips-backdrop', backdrop);
    else row.style.removeProperty('--ds-chips-backdrop');
    row.setAttribute('data-ds-glide', '');
    }
    function unbackChips(row) {
    const running = Array.from(row.children).flatMap((child) => child.getAnimations?.() ?? [])
    .filter((animation) => animation.id === 'ds-flip' && animation.playState !== 'finished' && animation.playState !== 'idle');
    if (running.length) {
    Promise.allSettled(running.map((animation) => animation.finished)).then(() => unbackChips(row));
    return;
    }
    row.removeAttribute('data-ds-glide');
    row.style.removeProperty('--ds-chips-backdrop');
    if (backedRows.delete(row)) {
    disown(row, 'attrs', 'data-ds-glide');
    disown(row, 'props', '--ds-chips-backdrop');
    }
    }
    function chipToggled(row) {
    const view = row.ownerDocument.defaultView;
    const bodies = Array.from(row.querySelectorAll('.ds-chip__body'));
    const children = Array.from(row.children);
    const tops = children.map((child) => child.getBoundingClientRect().top);
    const edges = bodies.map((body) => {
    const state = edgeGlides.has(body) ? edgeState(body) : { clip: 0, shift: 0 };
    stopEdge(body);
    return state;
    });
    let wrapped = false;
    let toggled = [];
    const glide = flip(row, () => {
    const slots = bodies.map((body) => {
    const style = view.getComputedStyle(body, '::after');
    return {
    body,
    from: parseFloat(style.width) || 0,
    timing: { duration: timeOf(style.transitionDuration), delay: timeOf(style.transitionDelay), easing: easingOf(style.transitionTimingFunction) },
    };
    });
    for (const body of bodies) {
    slotGlides.get(body)?.cancel();
    slotGlides.delete(body);
    }
    row.setAttribute('data-ds-snap', '');
    wrapped = children.some((child, index) => Math.abs(child.getBoundingClientRect().top - tops[index]) > 1);
    const ends = slots.map((slot) => parseFloat(view.getComputedStyle(slot.body, '::after').width) || 0);
    if (wrapped) {
    toggled = slots.map((slot, index) => {
    const grow = ends[index] - slot.from;
    const chip = slot.body.closest('.ds-chip');
    const next = chip?.parentElement === row ? chip.nextElementSibling : null;
    const same = next && Math.abs(next.getBoundingClientRect().top - chip.getBoundingClientRect().top) <= 1;
    return { body: slot.body, grow: grow + edges[index].clip, shift: edges[index].shift - grow, next: same ? next : null };
    }).filter((entry) => Math.abs(entry.grow) >= 0.5 || Math.abs(entry.shift) >= 0.5);
    }
    row.removeAttribute('data-ds-snap');
    if (wrapped) {
    backChips(row);
    return;
    }
    slots.forEach((slot, index) => glideSlot(slot.body, slot.from, ends[index], slot.timing));
    });
    if (!wrapped) return;
    for (const entry of toggled) {
    const timing = flipTiming(entry.next) ?? {
    duration: travelDuration(row, Math.abs(entry.grow)),
    easing: motionEasing(row, '--ds-ease-spatial'),
    };
    glideEdge(entry.body, entry.grow, entry.shift, timing);
    }
    glide.then(() => unbackChips(row), () => unbackChips(row));
    }
    function activates(target, key) {
    if (key === ' ') return target.tagName !== 'A';
    if (key === 'Enter') return !target.matches?.('input[type="checkbox"], input[type="radio"]');
    return false;
    }
    const MODIFIER_KEYS = new Set(['Shift', 'Control', 'Alt', 'Meta', 'AltGraph', 'CapsLock']);
    function watchInput(doc) {
    const key = Symbol.for('ds.input');
    if (typeof doc?.addEventListener !== 'function' || doc[key]) return;
    const record = (doc[key] = { last: '' });
    for (const type of ['pointerdown', 'mousedown']) doc.addEventListener(type, () => { record.last = 'pointer'; }, { capture: true, passive: true });
    doc.addEventListener('keydown', ( event) => {
    if (!MODIFIER_KEYS.has(event.key)) record.last = 'key';
    }, true);
    }
    function attach(root = document) {
    const existing = attached.get(root);
    if (existing) return existing;
    const element = (root.nodeType === 9 ? (root).documentElement : root);
    const marked = root.nodeType === 11 ? (root).querySelector('.ds-theme') ?? (root).host : element;
    const doc = element.ownerDocument;
    watchInput(doc);
    const blurs = new WeakMap();
    const onPointerDown = ( event) => {
    if (event.button !== 0 || event.isPrimary === false) return;
    const host = hostOf(event.target, element);
    if (!host || inactive(host)) return;
    const target = (event.target);
    if (target.closest?.(OWN_PRESS) && !host.matches(OWNS_INPUT)) return;
    if (pressesOther(host, target) || inPinnedRoom(host, target)) return;
    const press = start(host, event.clientX, event.clientY);
    if (!press) return;
    const pointer = event.pointerId;
    const stop = () => {
    host.removeEventListener('pointerleave', up);
    doc.removeEventListener('pointerup', up, true);
    doc.removeEventListener('pointercancel', up, true);
    doc.removeEventListener('gotpointercapture', captured, true);
    };
    const up = () => {
    stop();
    end(host);
    };
    const captured = ( capture) => {
    if (capture.pointerId !== pointer) return;
    const owner = (capture.target);
    if (owner && owner.nodeType === 1 && host.contains(owner)) return;
    up();
    };
    press.stop = stop;
    host.addEventListener('pointerleave', up);
    doc.addEventListener('pointerup', up, true);
    doc.addEventListener('pointercancel', up, true);
    doc.addEventListener('gotpointercapture', captured, true);
    };
    const onKeyDown = ( event) => {
    if (event.repeat || (event.key !== 'Enter' && event.key !== ' ')) return;
    const target = (event.target);
    const host = hostOf(target, element);
    if (!host || inactive(host) || !activates(target, event.key)) return;
    if (target !== host && !(host.matches(OWNS_INPUT) && target.tagName === 'INPUT') && !target.matches?.(HOST_LINK)) return;
    if (!start(host)) return;
    const stale = blurs.get(target);
    if (stale) target.removeEventListener('blur', stale);
    const leave = () => {
    blurs.delete(target);
    target.removeEventListener('blur', leave);
    end(host);
    };
    blurs.set(target, leave);
    target.addEventListener('blur', leave);
    };
    const onKeyUp = ( event) => {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const target = (event.target);
    const pending = blurs.get(target);
    if (pending) {
    target.removeEventListener('blur', pending);
    blurs.delete(target);
    }
    const host = hostOf(target, element);
    if (host) end(host);
    };
    const onTouchStart = () => {};
    let middleRow = null;
    const onRowClick = ( event) => {
    const own = event.type === 'auxclick' && middleRow !== null && middleRow.contains( (event.target));
    if (event.type === 'auxclick') middleRow = null;
    forwardRowClick(event, element, own);
    };
    const onRowMouseDown = ( event) => {
    middleRow = null;
    if (event.button !== 1 || event.defaultPrevented) return;
    const target = (event.target);
    const node = target?.nodeType === 1 ? target : target?.parentElement;
    const row = node?.closest?.(PINNED_ROW);
    if (!row || !element.contains(row) || !row.querySelector('a[href].ds-table__row-link') || node.closest(CONTROLS) || inPinnedRoom(row, node)) return;
    event.preventDefault();
    middleRow = row;
    };
    const onChange = ( event) => {
    const input = (event.target);
    if (!input?.matches?.('.ds-chip__input')) return;
    const row = input.closest('.ds-chips');
    if (row && element.contains(row)) chipToggled(row);
    };
    marked.classList.add('ds-ripples');
    element.addEventListener('pointerdown', onPointerDown);
    element.addEventListener('keydown', onKeyDown);
    element.addEventListener('keyup', onKeyUp);
    element.addEventListener('touchstart', onTouchStart, { passive: true });
    element.addEventListener('change', onChange);
    element.addEventListener('click', onRowClick);
    element.addEventListener('auxclick', onRowClick);
    element.addEventListener('mousedown', onRowMouseDown);
    const rootNode = root.nodeType === 9 || root.nodeType === 11 ? root : element.getRootNode?.() ?? doc;
    const guarded = guard( (rootNode));
    const handle = {
    destroy() {
    if (attached.get(root) !== handle) return;
    attached.delete(root);
    element.removeEventListener('pointerdown', onPointerDown);
    element.removeEventListener('keydown', onKeyDown);
    element.removeEventListener('keyup', onKeyUp);
    element.removeEventListener('touchstart', onTouchStart);
    element.removeEventListener('change', onChange);
    element.removeEventListener('click', onRowClick);
    element.removeEventListener('auxclick', onRowClick);
    element.removeEventListener('mousedown', onRowMouseDown);
    guarded.destroy();
    marked.classList.remove('ds-ripples');
    },
    release(host) {
    const hosts = host ? [host] : [...pressing].filter((item) => element.contains(item));
    for (const item of hosts) end(item);
    },
    };
    attached.set(root, handle);
    return handle;
    }
    define('ripple', {
    global(root) {
    const existed = attached.has(root);
    const handle = attach(root);
    return existed ? () => {} : () => handle.destroy();
    },
    });

    return Object.freeze({
      RIPPLE_HOSTS: RIPPLE_HOSTS,
      attach: attach
    });
  });

  add('icons', ['arrowRight', 'arrowLeft', 'arrowUp', 'arrowDown', 'chevronDown', 'chevronUp', 'chevronRight', 'chevronLeft', 'chevronFirst', 'chevronLast', 'cornerDownLeft', 'external', 'plus', 'close', 'minimize', 'menu', 'moreVertical', 'moreHorizontal', 'grip', 'search', 'filter', 'sort', 'compose', 'pencil', 'save', 'reset', 'sync', 'undo', 'history', 'copy', 'download', 'upload', 'clipboard', 'paperclip', 'printer', 'play', 'pause', 'stopCircle', 'trash', 'archive', 'logout', 'chat', 'send', 'bell', 'mic', 'micOff', 'volume', 'mail', 'phone', 'globe', 'link', 'flag', 'thumbUp', 'thumbDown', 'check', 'checkCircle', 'alert', 'alertCircle', 'info', 'helpCircle', 'eye', 'eyeOff', 'clock', 'ban', 'calendar', 'activity', 'wifi', 'wifiOff', 'home', 'panelLeft', 'keyboard', 'terminal', 'tag', 'sun', 'moon', 'overview', 'apps', 'list', 'columns', 'rows', 'barChart', 'settings', 'cpu', 'lock', 'key', 'shield', 'user', 'users', 'userOff', 'userPlus', 'ticket', 'file', 'fileText', 'fileCode', 'folder', 'box', 'building', 'mapPin', 'laptop', 'monitor', 'hardDrive', 'server', 'graduationCap', 'presentation', 'camera', 'video', 'image', 'leaf', 'wrench', 'sparkles', 'fileImage', 'fileArchive', 'fileSpreadsheet', 'fileJson', 'filePlus', 'fileCheck', 'fileDown', 'fileUp', 'files', 'folderOpen', 'folderPlus', 'cloudUpload', 'cloudDownload', 'lineChart', 'areaChart', 'barChartHorizontal', 'pieChart', 'donutChart', 'scatterChart', 'gauge', 'trendingUp', 'trendingDown', 'table', 'tableProperties', 'filterX', 'sortAscending', 'sortDescending', 'group', 'layoutGrid', 'listTree', 'kanban', 'bold', 'italic', 'code', 'quote', 'heading', 'listOrdered', 'unlink', 'redo', 'cut', 'paste', 'duplicate', 'gripHorizontal', 'move', 'resize', 'expandAll', 'collapseAll', 'fullscreen', 'fullscreenExit', 'splitView', 'panelRight', 'wrapText', 'shieldCheck', 'shieldAlert', 'unlock', 'fingerprint', 'verified', 'bug', 'bookmark', 'pin', 'star', 'starHalf', 'heart', 'smilePlus', 'bellOff', 'volumeOff', 'circleX', 'octagonAlert', 'circle', 'circleDot', 'square', 'squareCheck', 'squareMinus', 'toggleOn', 'toggleOff', 'userMinus', 'userCheck', 'userX', 'userCircle', 'idCard', 'network', 'database', 'cloud', 'cloudOff', 'memory', 'router', 'smartphone', 'tablet', 'terminalSquare', 'braces', 'gitBranch', 'gitCommit', 'gitMerge', 'gitPullRequest', 'gitCompare', 'webhook', 'plug', 'power', 'qrCode', 'barcode', 'scan', 'calendarDays', 'calendarCheck', 'calendarPlus', 'calendarRange', 'timer', 'hourglass', 'refresh', 'repeat', 'arrowUpRight', 'arrowUpLeft', 'arrowDownRight', 'arrowDownLeft', 'arrowLeftRight', 'cornerUpLeft', 'chevronsLeft', 'chevronsRight', 'login', 'launch', 'sparkle', 'wand', 'bot', 'chatText', 'chatDots', 'reply', 'audioLines', 'videoOff', 'skipForward', 'skipBack', 'atSign', 'creditCard', 'receipt', 'wallet', 'coupon', 'tags', 'cart', 'truck', 'map', 'route', 'compass', 'anchor', 'languages', 'palette', 'brush', 'contrast', 'mailOpen', 'inbox', 'archiveRestore', 'clipboardCheck', 'share', 'sliders', 'layers', 'stack', 'puzzle', 'rocket', 'lightbulb', 'beaker', 'book', 'newspaper', 'megaphone', 'gift', 'trophy', 'medal', 'target', 'zap', 'scrollText', 'ICON_SIZE', 'icon', 'iconElement', 'fill'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const arrowRight = '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>';
    const arrowLeft = '<path d="M19 12H5"/><path d="m12 19-7-7 7-7"/>';
    const arrowUp = '<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>';
    const arrowDown = '<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>';
    const chevronDown = '<path d="m6 9 6 6 6-6"/>';
    const chevronUp = '<path d="m18 15-6-6-6 6"/>';
    const chevronRight = '<path d="m9 18 6-6-6-6"/>';
    const chevronLeft = '<path d="m15 18-6-6 6-6"/>';
    const chevronFirst = '<path d="m17 18-6-6 6-6"/><path d="M7 6v12"/>';
    const chevronLast = '<path d="m7 18 6-6-6-6"/><path d="M17 6v12"/>';
    const cornerDownLeft = '<path d="M20 4v7a4 4 0 0 1-4 4H4"/><path d="m9 10-5 5 5 5"/>';
    const external = '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>';
    const plus = '<path d="M5 12h14"/><path d="M12 5v14"/>';
    const close = '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>';
    const minimize = '<path d="M5 12h14"/>';
    const menu = '<path d="M4 6h16"/><path d="M4 12h16"/><path d="M4 18h16"/>';
    const moreVertical = '<circle cx="12" cy="12" r="1"/><circle cx="12" cy="5" r="1"/><circle cx="12" cy="19" r="1"/>';
    const moreHorizontal = '<circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/><circle cx="5" cy="12" r="1"/>';
    const grip = '<circle cx="9" cy="12" r="1"/><circle cx="9" cy="5" r="1"/><circle cx="9" cy="19" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="5" r="1"/><circle cx="15" cy="19" r="1"/>';
    const search = '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>';
    const filter = '<path d="M3 6h18"/><path d="M7 12h10"/><path d="M10 18h4"/>';
    const sort = '<path d="m21 16-4 4-4-4"/><path d="M17 20V4"/><path d="m3 8 4-4 4 4"/><path d="M7 4v16"/>';
    const compose = '<path d="M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.4 2.6a2.1 2.1 0 0 1 3 3L12 15l-4 1 1-4z"/>';
    const pencil = '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/>';
    const save = '<path d="M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M17 21v-7a1 1 0 0 0-1-1H8a1 1 0 0 0-1 1v7"/><path d="M7 3v4a1 1 0 0 0 1 1h7"/>';
    const reset = '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/>';
    const sync = '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>';
    const undo = '<path d="M9 14 4 9l5-5"/><path d="M4 9h10.5a5.5 5.5 0 0 1 5.5 5.5a5.5 5.5 0 0 1-5.5 5.5H11"/>';
    const history = '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l4 2"/>';
    const copy = '<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2"/>';
    const download = '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5 5 5-5"/><path d="M12 15V3"/>';
    const upload = '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m17 8-5-5-5 5"/><path d="M12 3v12"/>';
    const clipboard = '<rect width="8" height="4" x="8" y="2" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>';
    const paperclip = '<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/>';
    const printer = '<path d="M6 18H4a2 2 0 0 1-2-2v-5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2h-2"/><path d="M6 9V3a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v6"/><rect width="12" height="8" x="6" y="14" rx="1"/>';
    const play = '<path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z"/>';
    const pause = '<rect width="4" height="16" x="14" y="4" rx="1"/><rect width="4" height="16" x="6" y="4" rx="1"/>';
    const stopCircle = '<circle cx="12" cy="12" r="10"/><rect width="6" height="6" x="9" y="9" rx="1"/>';
    const trash = '<path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M10 11v6"/><path d="M14 11v6"/>';
    const archive = '<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>';
    const logout = '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/>';
    const chat = '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>';
    const send = '<path d="M14.536 21.686a.5.5 0 0 0 .937-.024l6.5-19a.496.496 0 0 0-.635-.635l-19 6.5a.5.5 0 0 0-.024.937l7.93 3.18a2 2 0 0 1 1.112 1.11z"/><path d="m21.854 2.147-10.94 10.939"/>';
    const bell = '<path d="M10.268 21a2 2 0 0 0 3.464 0"/><path d="M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326z"/>';
    const mic = '<path d="M12 19v3"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><rect width="6" height="13" x="9" y="2" rx="3"/>';
    const micOff = '<path d="m2 2 20 20"/><path d="M18.89 13.23A7.12 7.12 0 0 0 19 12v-2"/><path d="M5 10v2a7 7 0 0 0 12 5"/><path d="M15 9.34V5a3 3 0 0 0-5.68-1.33"/><path d="M9 9v3a3 3 0 0 0 5.12 2.12"/><path d="M12 19v3"/>';
    const volume = '<path d="M11 4.702a.705.705 0 0 0-1.203-.498L6.413 7.587A1.4 1.4 0 0 1 5.416 8H3a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2.416a1.4 1.4 0 0 1 .997.413l3.383 3.384A.705.705 0 0 0 11 19.298z"/><path d="M16 9a5 5 0 0 1 0 6"/><path d="M19.364 18.364a9 9 0 0 0 0-12.728"/>';
    const mail = '<rect width="20" height="16" x="2" y="4" rx="2"/><path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/>';
    const phone = '<path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72c.13.96.36 1.9.7 2.81a2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45c.91.34 1.85.57 2.81.7A2 2 0 0 1 22 16.92z"/>';
    const globe = '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>';
    const link = '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>';
    const flag = '<path d="M4 22v-7"/><path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z"/>';
    const thumbUp = '<path d="M7 10v12"/><path d="M15 5.88 14 10h5.83a2 2 0 0 1 1.92 2.56l-2.33 8A2 2 0 0 1 17.5 22H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.76a2 2 0 0 0 1.79-1.11L12 2a3.13 3.13 0 0 1 3 3.88z"/>';
    const thumbDown = '<path d="M17 14V2"/><path d="M9 18.12 10 14H4.17a2 2 0 0 1-1.92-2.56l2.33-8A2 2 0 0 1 6.5 2H20a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-2.76a2 2 0 0 0-1.79 1.11L12 22a3.13 3.13 0 0 1-3-3.88z"/>';
    const check = '<path pathLength="1" d="M4 12l5 5L20 6"/>';
    const checkCircle = '<circle cx="12" cy="12" r="10"/><path pathLength="1" d="m9 12 2 2 4-4"/>';
    const alert = '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>';
    const alertCircle = '<circle cx="12" cy="12" r="10"/><path d="M12 8v4"/><path d="M12 16h.01"/>';
    const info = '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>';
    const helpCircle = '<circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/>';
    const eye = '<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>';
    const eyeOff = '<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/>';
    const clock = '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>';
    const ban = '<circle cx="12" cy="12" r="10"/><path d="m4.9 4.9 14.2 14.2"/>';
    const calendar = '<path d="M8 2v4"/><path d="M16 2v4"/><rect width="18" height="18" x="3" y="4" rx="2"/><path d="M3 10h18"/>';
    const activity = '<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>';
    const wifi = '<path d="M12 20h.01"/><path d="M2 8.82a15 15 0 0 1 20 0"/><path d="M5 12.859a10 10 0 0 1 14 0"/><path d="M8.5 16.429a5 5 0 0 1 7 0"/>';
    const wifiOff = '<path d="M12 20h.01"/><path d="M8.5 16.429a5 5 0 0 1 7 0"/><path d="M5 12.859a10 10 0 0 1 5.17-2.69"/><path d="M19 12.859a10 10 0 0 0-2.007-1.523"/><path d="M2 8.82a15 15 0 0 1 4.177-2.643"/><path d="M22 8.82a15 15 0 0 0-11.288-3.764"/><path d="m2 2 20 20"/>';
    const home = '<path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"/><path d="M3 10a2 2 0 0 1 .709-1.528l7-5.999a2 2 0 0 1 2.582 0l7 5.999A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>';
    const panelLeft = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>';
    const keyboard = '<path d="M10 8h.01"/><path d="M12 12h.01"/><path d="M14 8h.01"/><path d="M16 12h.01"/><path d="M18 8h.01"/><path d="M6 8h.01"/><path d="M7 16h10"/><path d="M8 12h.01"/><rect width="20" height="16" x="2" y="4" rx="2"/>';
    const terminal = '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>';
    const tag = '<path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z"/><circle cx="7.5" cy="7.5" r=".5"/>';
    const sun = '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>';
    const moon = '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9z"/>';
    const overview = '<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>';
    const apps = '<circle cx="5" cy="5" r="1"/><circle cx="12" cy="5" r="1"/><circle cx="19" cy="5" r="1"/><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/><circle cx="5" cy="19" r="1"/><circle cx="12" cy="19" r="1"/><circle cx="19" cy="19" r="1"/>';
    const list = '<path d="M3 12h.01"/><path d="M3 18h.01"/><path d="M3 6h.01"/><path d="M8 12h13"/><path d="M8 18h13"/><path d="M8 6h13"/>';
    const columns = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/><path d="M15 3v18"/>';
    const rows = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M21 9H3"/><path d="M21 15H3"/>';
    const barChart = '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/>';
    const settings = '<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>';
    const cpu = '<rect width="16" height="16" x="4" y="4" rx="2"/><rect width="6" height="6" x="9" y="9" rx="1"/><path d="M15 2v2"/><path d="M15 20v2"/><path d="M2 15h2"/><path d="M2 9h2"/><path d="M20 15h2"/><path d="M20 9h2"/><path d="M9 2v2"/><path d="M9 20v2"/>';
    const lock = '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>';
    const key = '<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5"/>';
    const shield = '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>';
    const user = '<circle cx="12" cy="8" r="5"/><path d="M20 21a8 8 0 0 0-16 0"/>';
    const users = '<path d="M18 21a8 8 0 0 0-16 0"/><circle cx="10" cy="8" r="5"/><path d="M22 20c0-3.37-2-6.5-4-8a5 5 0 0 0-.45-8.3"/>';
    const userOff = '<path d="M7.928 5.099a5 5 0 1 1 6.973 6.973"/><path d="M10.345 13.173A8 8 0 0 0 4 21"/><path d="m2 2 20 20"/>';
    const userPlus = '<path d="M2 21a8 8 0 0 1 13.292-6"/><circle cx="10" cy="8" r="5"/><path d="M19 16v6"/><path d="M22 19h-6"/>';
    const ticket = '<path d="M2 9a3 3 0 0 1 0 6v2a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-2a3 3 0 0 1 0-6V7a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2z"/><path d="M13 5v2"/><path d="M13 17v2"/><path d="M13 11v2"/>';
    const file = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/>';
    const fileText = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M16 13H8"/><path d="M16 17H8"/><path d="M10 9H8"/>';
    const fileCode = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m10 12.5-2 2.5 2 2.5"/><path d="m14 12.5 2 2.5-2 2.5"/>';
    const folder = '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2z"/>';
    const box = '<path d="M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z"/><path d="M12 22V12"/><path d="m3.3 7 7.703 4.734a2 2 0 0 0 1.994 0L20.7 7"/><path d="m7.5 4.27 9 5.15"/>';
    const building = '<path d="M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z"/><path d="M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2"/><path d="M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2"/><path d="M10 6h4"/><path d="M10 10h4"/><path d="M10 14h4"/><path d="M10 18h4"/>';
    const mapPin = '<path d="M20 10c0 4.993-5.539 10.193-7.399 11.799a1 1 0 0 1-1.202 0C9.539 20.193 4 14.993 4 10a8 8 0 0 1 16 0"/><circle cx="12" cy="10" r="3"/>';
    const laptop = '<path d="M20 16V7a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v9m16 0H4m16 0 1.28 2.55a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45L4 16"/>';
    const monitor = '<rect width="20" height="14" x="2" y="3" rx="2"/><path d="M8 21h8"/><path d="M12 17v4"/>';
    const hardDrive = '<path d="M22 12H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><path d="M6 16h.01"/><path d="M10 16h.01"/>';
    const server = '<rect width="20" height="8" x="2" y="2" rx="2"/><rect width="20" height="8" x="2" y="14" rx="2"/><path d="M6 6h.01"/><path d="M6 18h.01"/>';
    const graduationCap = '<path d="M21.42 10.922a1 1 0 0 0-.019-1.838L12.83 5.18a2 2 0 0 0-1.66 0L2.6 9.08a1 1 0 0 0 0 1.832l8.57 3.908a2 2 0 0 0 1.66 0z"/><path d="M22 10v6"/><path d="M6 12.5V16a6 3 0 0 0 12 0v-3.5"/>';
    const presentation = '<path d="M2 3h20"/><path d="M21 3v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V3"/><path d="m7 21 5-5 5 5"/>';
    const camera = '<path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/>';
    const video = '<path d="m16 13 5.223 3.482a.5.5 0 0 0 .777-.416V7.87a.5.5 0 0 0-.752-.432L16 10.5"/><rect x="2" y="6" width="14" height="12" rx="2"/>';
    const image = '<rect width="18" height="18" x="3" y="3" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/>';
    const leaf = '<path d="M11 20A7 7 0 0 1 9.8 6.1C15.5 5 17 4.48 19 2c1 2 2 4.18 2 8 0 5.5-4.78 10-10 10Z"/><path d="M2 21c0-3 1.85-5.36 5.08-6C9.5 14.52 12 13 13 12"/>';
    const wrench = '<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>';
    const sparkles = '<path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z"/><path d="M20 3v4"/><path d="M22 5h-4"/>';
    const fileImage = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><circle cx="10" cy="12" r="2"/><path d="m20 17-1.296-1.296a2.41 2.41 0 0 0-3.408 0L9 22"/>';
    const fileArchive = '<path d="M10 12v-1"/><path d="M10 18v-2"/><path d="M10 7V6"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M15.5 22H18a2 2 0 0 0 2-2V7l-5-5H6a2 2 0 0 0-2 2v16a2 2 0 0 0 .274 1.01"/><circle cx="10" cy="20" r="2"/>';
    const fileSpreadsheet = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M8 13h2"/><path d="M14 13h2"/><path d="M8 17h2"/><path d="M14 17h2"/>';
    const fileJson = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 12a1 1 0 0 0-1 1v1a1 1 0 0 1-1 1 1 1 0 0 1 1 1v1a1 1 0 0 0 1 1"/><path d="M14 18a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1 1 1 0 0 1-1-1v-1a1 1 0 0 0-1-1"/>';
    const filePlus = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M9 15h6"/><path d="M12 18v-6"/>';
    const fileCheck = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m9 15 2 2 4-4"/>';
    const fileDown = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M12 18v-6"/><path d="m9 15 3 3 3-3"/>';
    const fileUp = '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M12 12v6"/><path d="m15 15-3-3-3 3"/>';
    const files = '<path d="M20 7h-3a2 2 0 0 1-2-2V2"/><path d="M9 18a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h7l4 4v10a2 2 0 0 1-2 2Z"/><path d="M3 7.6v12.8A1.6 1.6 0 0 0 4.6 22h9.8"/>';
    const folderOpen = '<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>';
    const folderPlus = '<path d="M12 10v6"/><path d="M9 13h6"/><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>';
    const cloudUpload = '<path d="M12 13v8"/><path d="M4 14.899A7 7 0 1 1 15.71 8h1.79a4.5 4.5 0 0 1 2.5 8.242"/><path d="m8 17 4-4 4 4"/>';
    const cloudDownload = '<path d="M12 13v8l-4-4"/><path d="m12 21 4-4"/><path d="M4.393 15.269A7 7 0 1 1 15.71 8h1.79a4.5 4.5 0 0 1 2.436 8.284"/>';
    const lineChart = '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="m19 9-5 5-4-4-3 3"/>';
    const areaChart = '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M7 11.207a.5.5 0 0 1 .146-.353l2-2a.5.5 0 0 1 .708 0l3.292 3.292a.5.5 0 0 0 .708 0l4.292-4.292a.5.5 0 0 1 .854.353V16a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1z"/>';
    const barChartHorizontal = '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M7 16h8"/><path d="M7 11h12"/><path d="M7 6h3"/>';
    const pieChart = '<path d="M21 12c.552 0 1.005-.449.95-.998a10 10 0 0 0-8.953-8.951c-.55-.055-.998.398-.998.95v8a1 1 0 0 0 1 1z"/><path d="M21.21 15.89A10 10 0 1 1 8 2.83"/>';
    const donutChart = '<path d="M13 2.05A10 10 0 0 1 21.95 11H16.9A5 5 0 0 0 13 7.1z"/><path d="M21.8 14A10 10 0 1 1 10 2.2V7.42A5 5 0 1 0 16.58 14z"/>';
    const scatterChart = '<circle cx="7.5" cy="7.5" r=".5"/><circle cx="18.5" cy="5.5" r=".5"/><circle cx="11.5" cy="11.5" r=".5"/><circle cx="7.5" cy="16.5" r=".5"/><circle cx="17.5" cy="14.5" r=".5"/><path d="M3 3v16a2 2 0 0 0 2 2h16"/>';
    const gauge = '<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>';
    const trendingUp = '<path d="M22 7L13.5 15.5 8.5 10.5 2 17"/><path d="M16 7L22 7 22 13"/>';
    const trendingDown = '<path d="M22 17L13.5 8.5 8.5 13.5 2 7"/><path d="M16 17L22 17 22 11"/>';
    const table = '<path d="M12 3v18"/><rect width="18" height="18" x="3" y="3" rx="2"/><path d="M3 9h18"/><path d="M3 15h18"/>';
    const tableProperties = '<path d="M15 3v18"/><rect width="18" height="18" x="3" y="3" rx="2"/><path d="M21 9H3"/><path d="M21 15H3"/>';
    const filterX = '<path d="M12.531 3H3a1 1 0 0 0-.742 1.67l7.225 7.989A2 2 0 0 1 10 14v6a1 1 0 0 0 .553.895l2 1A1 1 0 0 0 14 21v-7a2 2 0 0 1 .517-1.341l.427-.473"/><path d="m16.5 3.5 5 5"/><path d="m21.5 3.5-5 5"/>';
    const sortAscending = '<path d="m3 8 4-4 4 4"/><path d="M7 4v16"/><path d="M11 12h4"/><path d="M11 16h7"/><path d="M11 20h10"/>';
    const sortDescending = '<path d="m3 16 4 4 4-4"/><path d="M7 20V4"/><path d="M11 4h10"/><path d="M11 8h7"/><path d="M11 12h4"/>';
    const group = '<path d="M3 7V5c0-1.1.9-2 2-2h2"/><path d="M17 3h2c1.1 0 2 .9 2 2v2"/><path d="M21 17v2c0 1.1-.9 2-2 2h-2"/><path d="M7 21H5c-1.1 0-2-.9-2-2v-2"/><rect width="7" height="5" x="7" y="7" rx="1"/><rect width="7" height="5" x="10" y="12" rx="1"/>';
    const layoutGrid = '<rect width="7" height="7" x="3" y="3" rx="1"/><rect width="7" height="7" x="14" y="3" rx="1"/><rect width="7" height="7" x="14" y="14" rx="1"/><rect width="7" height="7" x="3" y="14" rx="1"/>';
    const listTree = '<path d="M21 12h-8"/><path d="M21 6H8"/><path d="M21 18h-8"/><path d="M3 6v4c0 1.1.9 2 2 2h3"/><path d="M3 10v6c0 1.1.9 2 2 2h3"/>';
    const kanban = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M8 7v7"/><path d="M12 7v4"/><path d="M16 7v9"/>';
    const bold = '<path d="M6 12h9a4 4 0 0 1 0 8H7a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h7a4 4 0 0 1 0 8"/>';
    const italic = '<path d="M19 4h-9"/><path d="M14 20h-9"/><path d="M15 4L9 20"/>';
    const code = '<path d="M16 18L22 12 16 6"/><path d="M8 6L2 12 8 18"/>';
    const quote = '<path d="M17 6H3"/><path d="M21 12H8"/><path d="M21 18H8"/><path d="M3 12v6"/>';
    const heading = '<path d="M6 12h12"/><path d="M6 20V4"/><path d="M18 20V4"/>';
    const listOrdered = '<path d="M10 12h11"/><path d="M10 18h11"/><path d="M10 6h11"/><path d="M4 10h2"/><path d="M4 6h1v4"/><path d="M6 18H4c0-1 2-2 2-3s-1-1.5-2-1"/>';
    const unlink = '<path d="m18.84 12.25 1.72-1.71h-.02a5.004 5.004 0 0 0-.12-7.07 5.006 5.006 0 0 0-6.95 0l-1.72 1.71"/><path d="m5.17 11.75-1.71 1.71a5.004 5.004 0 0 0 .12 7.07 5.006 5.006 0 0 0 6.95 0l1.71-1.71"/><path d="M8 2v3"/><path d="M2 8h3"/><path d="M16 19v3"/><path d="M19 16h3"/>';
    const redo = '<path d="m15 14 5-5-5-5"/><path d="M20 9H9.5A5.5 5.5 0 0 0 4 14.5A5.5 5.5 0 0 0 9.5 20H13"/>';
    const cut = '<circle cx="6" cy="6" r="3"/><path d="M8.12 8.12 12 12"/><path d="M20 4 8.12 15.88"/><circle cx="6" cy="18" r="3"/><path d="M14.8 14.8 20 20"/>';
    const paste = '<path d="M15 2H9a1 1 0 0 0-1 1v2c0 .6.4 1 1 1h6c.6 0 1-.4 1-1V3c0-.6-.4-1-1-1Z"/><path d="M8 4H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2M16 4h2a2 2 0 0 1 2 2v2M11 14h10"/><path d="m17 10 4 4-4 4"/>';
    const duplicate = '<path d="M15 12v6"/><path d="M12 15h6"/><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>';
    const gripHorizontal = '<circle cx="12" cy="9" r="1"/><circle cx="19" cy="9" r="1"/><circle cx="5" cy="9" r="1"/><circle cx="12" cy="15" r="1"/><circle cx="19" cy="15" r="1"/><circle cx="5" cy="15" r="1"/>';
    const move = '<path d="M12 2v20"/><path d="m15 19-3 3-3-3"/><path d="m19 9 3 3-3 3"/><path d="M2 12h20"/><path d="m5 9-3 3 3 3"/><path d="m9 5 3-3 3 3"/>';
    const resize = '<path d="M19 13v6h-6"/><path d="M5 11V5h6"/><path d="m5 5 14 14"/>';
    const expandAll = '<path d="m7 15 5 5 5-5"/><path d="m7 9 5-5 5 5"/>';
    const collapseAll = '<path d="m7 20 5-5 5 5"/><path d="m7 4 5 5 5-5"/>';
    const fullscreen = '<path d="M8 3H5a2 2 0 0 0-2 2v3"/><path d="M21 8V5a2 2 0 0 0-2-2h-3"/><path d="M3 16v3a2 2 0 0 0 2 2h3"/><path d="M16 21h3a2 2 0 0 0 2-2v-3"/>';
    const fullscreenExit = '<path d="M8 3v3a2 2 0 0 1-2 2H3"/><path d="M21 8h-3a2 2 0 0 1-2-2V3"/><path d="M3 16h3a2 2 0 0 1 2 2v3"/><path d="M16 21v-3a2 2 0 0 1 2-2h3"/>';
    const splitView = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M12 3v18"/>';
    const panelRight = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M15 3v18"/>';
    const wrapText = '<path d="M3 6h18"/><path d="M3 12h15a3 3 0 1 1 0 6h-4"/><path d="M16 16L14 18 16 20"/><path d="M3 18h7"/>';
    const shieldCheck = '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>';
    const shieldAlert = '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="M12 8v4"/><path d="M12 16h.01"/>';
    const unlock = '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 9.9-1"/>';
    const fingerprint = '<path d="M12 10a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4"/><path d="M14 13.12c0 2.38 0 6.38-1 8.88"/><path d="M17.29 21.02c.12-.6.43-2.3.5-3.02"/><path d="M2 12a10 10 0 0 1 18-6"/><path d="M2 16h.01"/><path d="M21.8 16c.2-2 .131-5.354 0-6"/><path d="M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2"/><path d="M8.65 22c.21-.66.45-1.32.57-2"/><path d="M9 6.8a6 6 0 0 1 9 5.2v2"/>';
    const verified = '<path d="M3.85 8.62a4 4 0 0 1 4.78-4.77 4 4 0 0 1 6.74 0 4 4 0 0 1 4.78 4.78 4 4 0 0 1 0 6.74 4 4 0 0 1-4.77 4.78 4 4 0 0 1-6.75 0 4 4 0 0 1-4.78-4.77 4 4 0 0 1 0-6.76Z"/><path d="m9 12 2 2 4-4"/>';
    const bug = '<path d="m8 2 1.88 1.88"/><path d="M14.12 3.88 16 2"/><path d="M9 7.13v-1a3.003 3.003 0 1 1 6 0v1"/><path d="M12 20c-3.3 0-6-2.7-6-6v-3a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v3c0 3.3-2.7 6-6 6"/><path d="M12 20v-9"/><path d="M6.53 9C4.6 8.8 3 7.1 3 5"/><path d="M6 13H2"/><path d="M3 21c0-2.1 1.7-3.9 3.8-4"/><path d="M20.97 5c0 2.1-1.6 3.8-3.5 4"/><path d="M22 13h-4"/><path d="M17.2 17c2.1.1 3.8 1.9 3.8 4"/>';
    const bookmark = '<path d="m19 21-7-4-7 4V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v16z"/>';
    const pin = '<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/>';
    const star = '<path d="M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z"/>';
    const starHalf = '<path d="M12 18.338a2.1 2.1 0 0 0-.987.244L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.12 2.12 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.12 2.12 0 0 0 1.597-1.16l2.309-4.679A.53.53 0 0 1 12 2"/>';
    const heart = '<path d="M19 14c1.49-1.46 3-3.21 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.76 0-3 .5-4.5 2-1.5-1.5-2.74-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4.05 3 5.5l7 7Z"/>';
    const smilePlus = '<path d="M22 11v1a10 10 0 1 1-9-10"/><path d="M8 14s1.5 2 4 2 4-2 4-2"/><path d="M9 9h.01"/><path d="M15 9h.01"/><path d="M16 5h6"/><path d="M19 2v6"/>';
    const bellOff = '<path d="M10.268 21a2 2 0 0 0 3.464 0"/><path d="M17 17H4a1 1 0 0 1-.74-1.673C4.59 13.956 6 12.499 6 8a6 6 0 0 1 .258-1.742"/><path d="m2 2 20 20"/><path d="M8.668 3.01A6 6 0 0 1 18 8c0 2.687.77 4.653 1.707 6.05"/>';
    const volumeOff = '<path d="M11 4.702a.705.705 0 0 0-1.203-.498L6.413 7.587A1.4 1.4 0 0 1 5.416 8H3a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2.416a1.4 1.4 0 0 1 .997.413l3.383 3.384A.705.705 0 0 0 11 19.298z"/><path d="M22 9L16 15"/><path d="M16 9L22 15"/>';
    const circleX = '<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6"/><path d="m9 9 6 6"/>';
    const octagonAlert = '<path d="M12 16h.01"/><path d="M12 8v4"/><path d="M15.312 2a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586l-4.688-4.688A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2z"/>';
    const circle = '<circle cx="12" cy="12" r="10"/>';
    const circleDot = '<circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="1"/>';
    const square = '<rect width="18" height="18" x="3" y="3" rx="2"/>';
    const squareCheck = '<path d="M21 10.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h12.5"/><path d="m9 11 3 3L22 4"/>';
    const squareMinus = '<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M8 12h8"/>';
    const toggleOn = '<rect width="20" height="12" x="2" y="6" rx="6"/><circle cx="16" cy="12" r="2"/>';
    const toggleOff = '<rect width="20" height="12" x="2" y="6" rx="6"/><circle cx="8" cy="12" r="2"/>';
    const userMinus = '<path d="M2 21a8 8 0 0 1 13.292-6"/><circle cx="10" cy="8" r="5"/><path d="M22 19h-6"/>';
    const userCheck = '<path d="M2 21a8 8 0 0 1 13.292-6"/><circle cx="10" cy="8" r="5"/><path d="m16 19 2 2 4-4"/>';
    const userX = '<path d="M2 21a8 8 0 0 1 11.873-7"/><circle cx="10" cy="8" r="5"/><path d="m17 17 5 5"/><path d="m22 17-5 5"/>';
    const userCircle = '<path d="M18 20a6 6 0 0 0-12 0"/><circle cx="12" cy="10" r="4"/><circle cx="12" cy="12" r="10"/>';
    const idCard = '<path d="M16 10h2"/><path d="M16 14h2"/><path d="M6.17 15a3 3 0 0 1 5.66 0"/><circle cx="9" cy="11" r="2"/><rect x="2" y="5" width="20" height="14" rx="2"/>';
    const network = '<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>';
    const database = '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5V19A9 3 0 0 0 21 19V5"/><path d="M3 12A9 3 0 0 0 21 12"/>';
    const cloud = '<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>';
    const cloudOff = '<path d="m2 2 20 20"/><path d="M5.782 5.782A7 7 0 0 0 9 19h8.5a4.5 4.5 0 0 0 1.307-.193"/><path d="M21.532 16.5A4.5 4.5 0 0 0 17.5 10h-1.79A7.008 7.008 0 0 0 10 5.07"/>';
    const memory = '<path d="M6 19v-3"/><path d="M10 19v-3"/><path d="M14 19v-3"/><path d="M18 19v-3"/><path d="M8 11V9"/><path d="M16 11V9"/><path d="M12 11V9"/><path d="M2 15h20"/><path d="M2 7a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v1.1a2 2 0 0 0 0 3.837V17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-5.1a2 2 0 0 0 0-3.837Z"/>';
    const router = '<rect width="20" height="8" x="2" y="14" rx="2"/><path d="M6.01 18H6"/><path d="M10.01 18H10"/><path d="M15 10v4"/><path d="M17.84 7.17a4 4 0 0 0-5.66 0"/><path d="M20.66 4.34a8 8 0 0 0-11.31 0"/>';
    const smartphone = '<rect width="14" height="20" x="5" y="2" rx="2"/><path d="M12 18h.01"/>';
    const tablet = '<rect width="16" height="20" x="4" y="2" rx="2"/><path d="M12 18h.01"/>';
    const terminalSquare = '<path d="m7 11 2-2-2-2"/><path d="M11 13h4"/><rect width="18" height="18" x="3" y="3" rx="2"/>';
    const braces = '<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5c0 1.1.9 2 2 2h1"/><path d="M16 21h1a2 2 0 0 0 2-2v-5c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1"/>';
    const gitBranch = '<path d="M6 3v12"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>';
    const gitCommit = '<circle cx="12" cy="12" r="3"/><path d="M3 12h6"/><path d="M15 12h6"/>';
    const gitMerge = '<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M6 21V9a9 9 0 0 0 9 9"/>';
    const gitPullRequest = '<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 0 1 2 2v7"/><path d="M6 9v12"/>';
    const gitCompare = '<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 0 1 2 2v7"/><path d="M11 18H8a2 2 0 0 1-2-2V9"/>';
    const webhook = '<path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2"/><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"/><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8"/>';
    const plug = '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>';
    const power = '<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/>';
    const qrCode = '<rect width="5" height="5" x="3" y="3" rx="1"/><rect width="5" height="5" x="16" y="3" rx="1"/><rect width="5" height="5" x="3" y="16" rx="1"/><path d="M21 16h-3a2 2 0 0 0-2 2v3"/><path d="M21 21v.01"/><path d="M12 7v3a2 2 0 0 1-2 2H7"/><path d="M3 12h.01"/><path d="M12 3h.01"/><path d="M12 16v.01"/><path d="M16 12h1"/><path d="M21 12v.01"/><path d="M12 21v-1"/>';
    const barcode = '<path d="M3 5v14"/><path d="M8 5v14"/><path d="M12 5v14"/><path d="M17 5v14"/><path d="M21 5v14"/>';
    const scan = '<path d="M3 7V5a2 2 0 0 1 2-2h2"/><path d="M17 3h2a2 2 0 0 1 2 2v2"/><path d="M21 17v2a2 2 0 0 1-2 2h-2"/><path d="M7 21H5a2 2 0 0 1-2-2v-2"/><path d="M7 12h10"/>';
    const calendarDays = '<path d="M8 2v4"/><path d="M16 2v4"/><rect width="18" height="18" x="3" y="4" rx="2"/><path d="M3 10h18"/><path d="M8 14h.01"/><path d="M12 14h.01"/><path d="M16 14h.01"/><path d="M8 18h.01"/><path d="M12 18h.01"/><path d="M16 18h.01"/>';
    const calendarCheck = '<path d="M8 2v4"/><path d="M16 2v4"/><rect width="18" height="18" x="3" y="4" rx="2"/><path d="M3 10h18"/><path d="m9 16 2 2 4-4"/>';
    const calendarPlus = '<path d="M8 2v4"/><path d="M16 2v4"/><path d="M21 13V6a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h8"/><path d="M3 10h18"/><path d="M16 19h6"/><path d="M19 16v6"/>';
    const calendarRange = '<rect width="18" height="18" x="3" y="4" rx="2"/><path d="M16 2v4"/><path d="M3 10h18"/><path d="M8 2v4"/><path d="M17 14h-6"/><path d="M13 18H7"/><path d="M7 14h.01"/><path d="M17 18h.01"/>';
    const timer = '<path d="M10 2h4"/><path d="M12 14L15 11"/><circle cx="12" cy="14" r="8"/>';
    const hourglass = '<path d="M5 22h14"/><path d="M5 2h14"/><path d="M17 22v-4.172a2 2 0 0 0-.586-1.414L12 12l-4.414 4.414A2 2 0 0 0 7 17.828V22"/><path d="M7 2v4.172a2 2 0 0 0 .586 1.414L12 12l4.414-4.414A2 2 0 0 0 17 6.172V2"/>';
    const refresh = '<path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/>';
    const repeat = '<path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/>';
    const arrowUpRight = '<path d="M7 7h10v10"/><path d="M7 17 17 7"/>';
    const arrowUpLeft = '<path d="M7 17V7h10"/><path d="M17 17 7 7"/>';
    const arrowDownRight = '<path d="m7 7 10 10"/><path d="M17 7v10H7"/>';
    const arrowDownLeft = '<path d="M17 7 7 17"/><path d="M17 17H7V7"/>';
    const arrowLeftRight = '<path d="M8 3 4 7l4 4"/><path d="M4 7h16"/><path d="m16 21 4-4-4-4"/><path d="M20 17H4"/>';
    const cornerUpLeft = '<path d="M9 14L4 9 9 4"/><path d="M20 20v-7a4 4 0 0 0-4-4H4"/>';
    const chevronsLeft = '<path d="m11 17-5-5 5-5"/><path d="m18 17-5-5 5-5"/>';
    const chevronsRight = '<path d="m6 17 5-5-5-5"/><path d="m13 17 5-5-5-5"/>';
    const login = '<path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/><path d="M10 17L15 12 10 7"/><path d="M15 12h-12"/>';
    const launch = '<path d="M21 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h6"/><path d="m21 3-9 9"/><path d="M15 3h6v6"/>';
    const sparkle = '<path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z"/>';
    const wand = '<path d="m21.64 3.64-1.28-1.28a1.21 1.21 0 0 0-1.72 0L2.36 18.64a1.21 1.21 0 0 0 0 1.72l1.28 1.28a1.2 1.2 0 0 0 1.72 0L21.64 5.36a1.2 1.2 0 0 0 0-1.72"/><path d="m14 7 3 3"/><path d="M5 6v4"/><path d="M19 14v4"/><path d="M10 2v2"/><path d="M7 8H3"/><path d="M21 16h-4"/><path d="M11 3H9"/>';
    const bot = '<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/>';
    const chatText = '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/><path d="M13 8H7"/><path d="M17 12H7"/>';
    const chatDots = '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/><path d="M8 10h.01"/><path d="M12 10h.01"/><path d="M16 10h.01"/>';
    const reply = '<path d="M9 17L4 12 9 7"/><path d="M20 18v-2a4 4 0 0 0-4-4H4"/>';
    const audioLines = '<path d="M2 10v3"/><path d="M6 6v11"/><path d="M10 3v18"/><path d="M14 8v7"/><path d="M18 5v13"/><path d="M22 10v3"/>';
    const videoOff = '<path d="M10.66 6H14a2 2 0 0 1 2 2v2.5l5.248-3.062A.5.5 0 0 1 22 7.87v8.196"/><path d="M16 16a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h2"/><path d="m2 2 20 20"/>';
    const skipForward = '<path d="M21 4v16"/><path d="M6.029 4.285A2 2 0 0 0 3 6v12a2 2 0 0 0 3.029 1.715l9.997-5.998a2 2 0 0 0 .003-3.432z"/>';
    const skipBack = '<path d="M3 20V4"/><path d="M17.971 4.285A2 2 0 0 1 21 6v12a2 2 0 0 1-3.029 1.715l-9.997-5.998a2 2 0 0 1-.003-3.432z"/>';
    const atSign = '<circle cx="12" cy="12" r="4"/><path d="M16 8v5a3 3 0 0 0 6 0v-1a10 10 0 1 0-4 8"/>';
    const creditCard = '<rect width="20" height="14" x="2" y="5" rx="2"/><path d="M2 10h20"/>';
    const receipt = '<path d="M4 2v20l2-1 2 1 2-1 2 1 2-1 2 1 2-1 2 1V2l-2 1-2-1-2 1-2-1-2 1-2-1-2 1Z"/><path d="M14 8H8"/><path d="M16 12H8"/><path d="M13 16H8"/>';
    const wallet = '<path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1"/><path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4"/>';
    const coupon = '<path d="M2 9a3 3 0 1 1 0 6v2a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-2a3 3 0 1 1 0-6V7a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2Z"/><path d="M9 9h.01"/><path d="m15 9-6 6"/><path d="M15 15h.01"/>';
    const tags = '<path d="m15 5 6.3 6.3a2.4 2.4 0 0 1 0 3.4L17 19"/><path d="M9.586 5.586A2 2 0 0 0 8.172 5H3a1 1 0 0 0-1 1v5.172a2 2 0 0 0 .586 1.414L8.29 18.29a2.426 2.426 0 0 0 3.42 0l3.58-3.58a2.426 2.426 0 0 0 0-3.42z"/><circle cx="6.5" cy="9.5" r=".5"/>';
    const cart = '<circle cx="8" cy="21" r="1"/><circle cx="19" cy="21" r="1"/><path d="M2.05 2.05h2l2.66 12.42a2 2 0 0 0 2 1.58h9.78a2 2 0 0 0 1.95-1.57l1.65-7.43H5.12"/>';
    const truck = '<path d="M14 18V6a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2v11a1 1 0 0 0 1 1h2"/><path d="M15 18H9"/><path d="M19 18h2a1 1 0 0 0 1-1v-3.65a1 1 0 0 0-.22-.624l-3.48-4.35A1 1 0 0 0 17.52 8H14"/><circle cx="17" cy="18" r="2"/><circle cx="7" cy="18" r="2"/>';
    const map = '<path d="M14.106 5.553a2 2 0 0 0 1.788 0l3.659-1.83A1 1 0 0 1 21 4.619v12.764a1 1 0 0 1-.553.894l-4.553 2.277a2 2 0 0 1-1.788 0l-4.212-2.106a2 2 0 0 0-1.788 0l-3.659 1.83A1 1 0 0 1 3 19.381V6.618a1 1 0 0 1 .553-.894l4.553-2.277a2 2 0 0 1 1.788 0z"/><path d="M15 5.764v15"/><path d="M9 3.236v15"/>';
    const route = '<circle cx="6" cy="19" r="3"/><path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"/><circle cx="18" cy="5" r="3"/>';
    const compass = '<path d="m16.24 7.76-1.804 5.411a2 2 0 0 1-1.265 1.265L7.76 16.24l1.804-5.411a2 2 0 0 1 1.265-1.265z"/><circle cx="12" cy="12" r="10"/>';
    const anchor = '<path d="M12 22V8"/><path d="M5 12H2a10 10 0 0 0 20 0h-3"/><circle cx="12" cy="5" r="3"/>';
    const languages = '<path d="m5 8 6 6"/><path d="m4 14 6-6 2-3"/><path d="M2 5h12"/><path d="M7 2h1"/><path d="m22 22-5-10-5 10"/><path d="M14 18h6"/>';
    const palette = '<circle cx="13.5" cy="6.5" r=".5"/><circle cx="17.5" cy="10.5" r=".5"/><circle cx="8.5" cy="7.5" r=".5"/><circle cx="6.5" cy="12.5" r=".5"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z"/>';
    const brush = '<path d="m14.622 17.897-10.68-2.913"/><path d="M18.376 2.622a1 1 0 1 1 3.002 3.002L17.36 9.643a.5.5 0 0 0 0 .707l.944.944a2.41 2.41 0 0 1 0 3.408l-.944.944a.5.5 0 0 1-.707 0L8.354 7.348a.5.5 0 0 1 0-.707l.944-.944a2.41 2.41 0 0 1 3.408 0l.944.944a.5.5 0 0 0 .707 0z"/><path d="M9 8c-1.804 2.71-3.97 3.46-6.583 3.948a.507.507 0 0 0-.302.819l7.32 8.883a1 1 0 0 0 1.185.204C12.735 20.405 16 16.792 16 15"/>';
    const contrast = '<circle cx="12" cy="12" r="10"/><path d="M12 18a6 6 0 0 0 0-12v12z"/>';
    const mailOpen = '<path d="M21.2 8.4c.5.38.8.97.8 1.6v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V10a2 2 0 0 1 .8-1.6l8-6a2 2 0 0 1 2.4 0l8 6Z"/><path d="m22 10-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 10"/>';
    const inbox = '<path d="M22 12L16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>';
    const archiveRestore = '<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h2"/><path d="M20 8v11a2 2 0 0 1-2 2h-2"/><path d="m9 15 3-3 3 3"/><path d="M12 12v9"/>';
    const clipboardCheck = '<rect width="8" height="4" x="8" y="2" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="m9 14 2 2 4-4"/>';
    const share = '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><path d="M8.59 13.51L15.42 17.49"/><path d="M15.41 6.51L8.59 10.49"/>';
    const sliders = '<path d="M21 4h-7"/><path d="M10 4h-7"/><path d="M21 12h-9"/><path d="M8 12h-5"/><path d="M21 20h-5"/><path d="M12 20h-9"/><path d="M14 2v4"/><path d="M8 10v4"/><path d="M16 18v4"/>';
    const layers = '<path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>';
    const stack = '<path d="m16.02 12 5.48 3.13a1 1 0 0 1 0 1.74L13 21.74a2 2 0 0 1-2 0l-8.5-4.87a1 1 0 0 1 0-1.74L7.98 12"/><path d="M13 13.74a2 2 0 0 1-2 0L2.5 8.87a1 1 0 0 1 0-1.74L11 2.26a2 2 0 0 1 2 0l8.5 4.87a1 1 0 0 1 0 1.74Z"/>';
    const puzzle = '<path d="M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 15.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0 .474-1.68l-1.683-1.682a2.414 2.414 0 0 1 0-3.414L4.39 8.61a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1-.474-1.68l1.683-1.682a2.414 2.414 0 0 1 3.414 0z"/>';
    const rocket = '<path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z"/><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z"/><path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0"/><path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5"/>';
    const lightbulb = '<path d="M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5"/><path d="M9 18h6"/><path d="M10 22h4"/>';
    const beaker = '<path d="M10 2v7.527a2 2 0 0 1-.211.896L4.72 20.55a1 1 0 0 0 .9 1.45h12.76a1 1 0 0 0 .9-1.45l-5.069-10.127A2 2 0 0 1 14 9.527V2"/><path d="M8.5 2h7"/><path d="M7 16h10"/>';
    const book = '<path d="M12 7v14"/><path d="M3 18a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h5a4 4 0 0 1 4 4 4 4 0 0 1 4-4h5a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1h-6a3 3 0 0 0-3 3 3 3 0 0 0-3-3z"/>';
    const newspaper = '<path d="M4 22h16a2 2 0 0 0 2-2V4a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v16a2 2 0 0 1-2 2Zm0 0a2 2 0 0 1-2-2v-9c0-1.1.9-2 2-2h2"/><path d="M18 14h-8"/><path d="M15 18h-5"/><path d="M10 6h8v4h-8V6Z"/>';
    const megaphone = '<path d="m3 11 18-5v12L3 14v-3z"/><path d="M11.6 16.8a3 3 0 1 1-5.8-1.6"/>';
    const gift = '<rect x="3" y="8" width="18" height="4" rx="1"/><path d="M12 8v13"/><path d="M19 12v7a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2v-7"/><path d="M7.5 8a2.5 2.5 0 0 1 0-5A4.8 8 0 0 1 12 8a4.8 8 0 0 1 4.5-5 2.5 2.5 0 0 1 0 5"/>';
    const trophy = '<path d="M6 9H4.5a2.5 2.5 0 0 1 0-5H6"/><path d="M18 9h1.5a2.5 2.5 0 0 0 0-5H18"/><path d="M4 22h16"/><path d="M10 14.66V17c0 .55-.47.98-.97 1.21C7.85 18.75 7 20.24 7 22"/><path d="M14 14.66V17c0 .55.47.98.97 1.21C16.15 18.75 17 20.24 17 22"/><path d="M18 2H6v7a6 6 0 0 0 12 0V2Z"/>';
    const medal = '<path d="M7.21 15 2.66 7.14a2 2 0 0 1 .13-2.2L4.4 2.8A2 2 0 0 1 6 2h12a2 2 0 0 1 1.6.8l1.6 2.14a2 2 0 0 1 .14 2.2L16.79 15"/><path d="M11 12 5.12 2.2"/><path d="m13 12 5.88-9.8"/><path d="M8 7h8"/><circle cx="12" cy="17" r="5"/><path d="M12 18v-2h-.5"/>';
    const target = '<circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="6"/><circle cx="12" cy="12" r="2"/>';
    const zap = '<path d="M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z"/>';
    const scrollText = '<path d="M15 12h-5"/><path d="M15 8h-5"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3"/>';
    const ICON_SIZE = 20;
    const SVG_NS = 'http://www.w3.org/2000/svg';
    const ELEMENT = /<([a-z]+)((?:\s+[\w:-]+="[^"]*")*)\s*\/>/g;
    const ATTRIBUTE = /([\w:-]+)="([^"]*)"/g;
    function checkGlyph(body) {
    if (typeof body !== 'string' || body.charAt(0) !== '<') throw new Error('Unknown icon: ' + body);
    }
    const ESCAPES = { '&': '&amp;', '"': '&quot;', '<': '&lt;', '>': '&gt;' };
    function escapeAttribute(value) {
    return String(value).replace(/[&"<>]/g, (char) => ESCAPES[char]);
    }
    function icon(body, size, className) {
    checkGlyph(body);
    const px = size || ICON_SIZE;
    const cls = className ? ` class="${escapeAttribute(className)}"` : '';
    return `<svg${cls} width="${px}" height="${px}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
    }
    function iconElement(body, size, className, doc) {
    checkGlyph(body);
    const d = doc || document;
    const px = String(size || ICON_SIZE);
    const svg = d.createElementNS(SVG_NS, 'svg');
    const attributes = [
    ['width', px], ['height', px], ['viewBox', '0 0 24 24'], ['fill', 'none'], ['stroke', 'currentColor'],
    ['stroke-width', '2'], ['stroke-linecap', 'round'], ['stroke-linejoin', 'round'], ['aria-hidden', 'true'],
    ];
    if (className) attributes.unshift(['class', className]);
    for (const pair of attributes) svg.setAttribute(pair[0], pair[1]);
    for (const element of body.matchAll(ELEMENT)) {
    const child = d.createElementNS(SVG_NS, element[1]);
    for (const attribute of element[2].matchAll(ATTRIBUTE)) child.setAttribute(attribute[1], attribute[2]);
    svg.appendChild(child);
    }
    return svg;
    }
    const GLYPHS = {
    arrowRight, arrowLeft, arrowUp, arrowDown, chevronDown, chevronUp, chevronRight, chevronLeft, chevronFirst, chevronLast, cornerDownLeft, external, plus, close, minimize, menu, moreVertical, moreHorizontal, grip, search, filter, sort, compose, pencil, save, reset, sync, undo, history, copy, download, upload, clipboard, paperclip, printer, play, pause, stopCircle, trash, archive, logout, chat, send, bell, mic, micOff, volume, mail, phone, globe, link, flag, thumbUp, thumbDown, check, checkCircle, alert, alertCircle, info, helpCircle, eye, eyeOff, clock, ban, calendar, activity, wifi, wifiOff, home, panelLeft, keyboard, terminal, tag, sun, moon, overview, apps, list, columns, rows, barChart, settings, cpu, lock, key, shield, user, users, userPlus, userOff, ticket, file, fileText, fileCode, folder, box, building, mapPin, laptop, monitor, hardDrive, server, graduationCap, presentation, camera, video, image, leaf, wrench, sparkles, fileImage, fileArchive, fileSpreadsheet, fileJson, filePlus, fileCheck, fileDown, fileUp, files, folderOpen, folderPlus, cloudUpload, cloudDownload, lineChart, areaChart, barChartHorizontal, pieChart, donutChart, scatterChart, gauge, trendingUp, trendingDown, table, tableProperties, filterX, sortAscending, sortDescending, group, layoutGrid, listTree, kanban, bold, italic, code, quote, heading, listOrdered, unlink, redo, cut, paste, duplicate, gripHorizontal, move, resize, expandAll, collapseAll, fullscreen, fullscreenExit, splitView, panelRight, wrapText, shieldCheck, shieldAlert, unlock, fingerprint, verified, bug, bookmark, pin, star, starHalf, heart, smilePlus, bellOff, volumeOff, circleX, octagonAlert, circle, circleDot, square, squareCheck, squareMinus, toggleOn, toggleOff, userMinus, userCheck, userX, userCircle, idCard, network, database, cloud, cloudOff, memory, router, smartphone, tablet, terminalSquare, braces, gitBranch, gitCommit, gitMerge, gitPullRequest, gitCompare, webhook, plug, power, qrCode, barcode, scan, calendarDays, calendarCheck, calendarPlus, calendarRange, timer, hourglass, refresh, repeat, arrowUpRight, arrowUpLeft, arrowDownRight, arrowDownLeft, arrowLeftRight, cornerUpLeft, chevronsLeft, chevronsRight, login, launch, sparkle, wand, bot, chatText, chatDots, reply, audioLines, videoOff, skipForward, skipBack, atSign, creditCard, receipt, wallet, coupon, tags, cart, truck, map, route, compass, anchor, languages, palette, brush, contrast, mailOpen, inbox, archiveRestore, clipboardCheck, share, sliders, layers, stack, puzzle, rocket, lightbulb, beaker, book, newspaper, megaphone, gift, trophy, medal, target, zap, scrollText
    };
    const filled = new WeakMap();
    function fill(root) {
    const scope = root || document;
    const doc = scope.nodeType === 9 ? (scope) : scope.ownerDocument;
    const targets = Array.from(scope.querySelectorAll('[data-icon]'));
    if (scope.nodeType === 1 && (scope).hasAttribute('data-icon')) targets.unshift( (scope));
    const unknown = [];
    let drawn = 0;
    for (const target of targets) {
    const name = target.getAttribute('data-icon') || '';
    const body = Object.prototype.hasOwnProperty.call(GLYPHS, name) ? GLYPHS[name] : undefined;
    if (!body) {
    unknown.push(name);
    continue;
    }
    const size = Number(target.getAttribute('data-icon-size')) || ICON_SIZE;
    const key = name + ' ' + size;
    const last = filled.get(target);
    const place = target.getAttribute('data-icon-place');
    if (place === 'start' || place === 'end') {
    const kept = last && last.svg.parentNode === target ? last.svg : null;
    if (kept && last.key === key) continue;
    const svg = iconElement(body, size, undefined, doc);
    if (kept) kept.replaceWith(svg);
    else if (place === 'start') target.prepend(svg);
    else target.append(svg);
    filled.set(target, { key, svg });
    drawn += 1;
    continue;
    }
    if (last && last.key === key && target.childNodes.length === 1 && target.firstChild === last.svg) continue;
    const svg = iconElement(body, size, undefined, doc);
    target.replaceChildren(svg);
    filled.set(target, { key, svg });
    drawn += 1;
    }
    if (unknown.length) throw new Error('Unknown icon: ' + unknown.join(', '));
    return drawn;
    }

    return Object.freeze({
      arrowRight: arrowRight,
      arrowLeft: arrowLeft,
      arrowUp: arrowUp,
      arrowDown: arrowDown,
      chevronDown: chevronDown,
      chevronUp: chevronUp,
      chevronRight: chevronRight,
      chevronLeft: chevronLeft,
      chevronFirst: chevronFirst,
      chevronLast: chevronLast,
      cornerDownLeft: cornerDownLeft,
      external: external,
      plus: plus,
      close: close,
      minimize: minimize,
      menu: menu,
      moreVertical: moreVertical,
      moreHorizontal: moreHorizontal,
      grip: grip,
      search: search,
      filter: filter,
      sort: sort,
      compose: compose,
      pencil: pencil,
      save: save,
      reset: reset,
      sync: sync,
      undo: undo,
      history: history,
      copy: copy,
      download: download,
      upload: upload,
      clipboard: clipboard,
      paperclip: paperclip,
      printer: printer,
      play: play,
      pause: pause,
      stopCircle: stopCircle,
      trash: trash,
      archive: archive,
      logout: logout,
      chat: chat,
      send: send,
      bell: bell,
      mic: mic,
      micOff: micOff,
      volume: volume,
      mail: mail,
      phone: phone,
      globe: globe,
      link: link,
      flag: flag,
      thumbUp: thumbUp,
      thumbDown: thumbDown,
      check: check,
      checkCircle: checkCircle,
      alert: alert,
      alertCircle: alertCircle,
      info: info,
      helpCircle: helpCircle,
      eye: eye,
      eyeOff: eyeOff,
      clock: clock,
      ban: ban,
      calendar: calendar,
      activity: activity,
      wifi: wifi,
      wifiOff: wifiOff,
      home: home,
      panelLeft: panelLeft,
      keyboard: keyboard,
      terminal: terminal,
      tag: tag,
      sun: sun,
      moon: moon,
      overview: overview,
      apps: apps,
      list: list,
      columns: columns,
      rows: rows,
      barChart: barChart,
      settings: settings,
      cpu: cpu,
      lock: lock,
      key: key,
      shield: shield,
      user: user,
      users: users,
      userOff: userOff,
      userPlus: userPlus,
      ticket: ticket,
      file: file,
      fileText: fileText,
      fileCode: fileCode,
      folder: folder,
      box: box,
      building: building,
      mapPin: mapPin,
      laptop: laptop,
      monitor: monitor,
      hardDrive: hardDrive,
      server: server,
      graduationCap: graduationCap,
      presentation: presentation,
      camera: camera,
      video: video,
      image: image,
      leaf: leaf,
      wrench: wrench,
      sparkles: sparkles,
      fileImage: fileImage,
      fileArchive: fileArchive,
      fileSpreadsheet: fileSpreadsheet,
      fileJson: fileJson,
      filePlus: filePlus,
      fileCheck: fileCheck,
      fileDown: fileDown,
      fileUp: fileUp,
      files: files,
      folderOpen: folderOpen,
      folderPlus: folderPlus,
      cloudUpload: cloudUpload,
      cloudDownload: cloudDownload,
      lineChart: lineChart,
      areaChart: areaChart,
      barChartHorizontal: barChartHorizontal,
      pieChart: pieChart,
      donutChart: donutChart,
      scatterChart: scatterChart,
      gauge: gauge,
      trendingUp: trendingUp,
      trendingDown: trendingDown,
      table: table,
      tableProperties: tableProperties,
      filterX: filterX,
      sortAscending: sortAscending,
      sortDescending: sortDescending,
      group: group,
      layoutGrid: layoutGrid,
      listTree: listTree,
      kanban: kanban,
      bold: bold,
      italic: italic,
      code: code,
      quote: quote,
      heading: heading,
      listOrdered: listOrdered,
      unlink: unlink,
      redo: redo,
      cut: cut,
      paste: paste,
      duplicate: duplicate,
      gripHorizontal: gripHorizontal,
      move: move,
      resize: resize,
      expandAll: expandAll,
      collapseAll: collapseAll,
      fullscreen: fullscreen,
      fullscreenExit: fullscreenExit,
      splitView: splitView,
      panelRight: panelRight,
      wrapText: wrapText,
      shieldCheck: shieldCheck,
      shieldAlert: shieldAlert,
      unlock: unlock,
      fingerprint: fingerprint,
      verified: verified,
      bug: bug,
      bookmark: bookmark,
      pin: pin,
      star: star,
      starHalf: starHalf,
      heart: heart,
      smilePlus: smilePlus,
      bellOff: bellOff,
      volumeOff: volumeOff,
      circleX: circleX,
      octagonAlert: octagonAlert,
      circle: circle,
      circleDot: circleDot,
      square: square,
      squareCheck: squareCheck,
      squareMinus: squareMinus,
      toggleOn: toggleOn,
      toggleOff: toggleOff,
      userMinus: userMinus,
      userCheck: userCheck,
      userX: userX,
      userCircle: userCircle,
      idCard: idCard,
      network: network,
      database: database,
      cloud: cloud,
      cloudOff: cloudOff,
      memory: memory,
      router: router,
      smartphone: smartphone,
      tablet: tablet,
      terminalSquare: terminalSquare,
      braces: braces,
      gitBranch: gitBranch,
      gitCommit: gitCommit,
      gitMerge: gitMerge,
      gitPullRequest: gitPullRequest,
      gitCompare: gitCompare,
      webhook: webhook,
      plug: plug,
      power: power,
      qrCode: qrCode,
      barcode: barcode,
      scan: scan,
      calendarDays: calendarDays,
      calendarCheck: calendarCheck,
      calendarPlus: calendarPlus,
      calendarRange: calendarRange,
      timer: timer,
      hourglass: hourglass,
      refresh: refresh,
      repeat: repeat,
      arrowUpRight: arrowUpRight,
      arrowUpLeft: arrowUpLeft,
      arrowDownRight: arrowDownRight,
      arrowDownLeft: arrowDownLeft,
      arrowLeftRight: arrowLeftRight,
      cornerUpLeft: cornerUpLeft,
      chevronsLeft: chevronsLeft,
      chevronsRight: chevronsRight,
      login: login,
      launch: launch,
      sparkle: sparkle,
      wand: wand,
      bot: bot,
      chatText: chatText,
      chatDots: chatDots,
      reply: reply,
      audioLines: audioLines,
      videoOff: videoOff,
      skipForward: skipForward,
      skipBack: skipBack,
      atSign: atSign,
      creditCard: creditCard,
      receipt: receipt,
      wallet: wallet,
      coupon: coupon,
      tags: tags,
      cart: cart,
      truck: truck,
      map: map,
      route: route,
      compass: compass,
      anchor: anchor,
      languages: languages,
      palette: palette,
      brush: brush,
      contrast: contrast,
      mailOpen: mailOpen,
      inbox: inbox,
      archiveRestore: archiveRestore,
      clipboardCheck: clipboardCheck,
      share: share,
      sliders: sliders,
      layers: layers,
      stack: stack,
      puzzle: puzzle,
      rocket: rocket,
      lightbulb: lightbulb,
      beaker: beaker,
      book: book,
      newspaper: newspaper,
      megaphone: megaphone,
      gift: gift,
      trophy: trophy,
      medal: medal,
      target: target,
      zap: zap,
      scrollText: scrollText,
      ICON_SIZE: ICON_SIZE,
      icon: icon,
      iconElement: iconElement,
      fill: fill
    });
  });

  add('position', ['round', 'pxOf', 'computePlacement', 'viewportBounds', 'clippedBounds', 'fixedOrigin', 'dropProbe', 'place', 'supportsAnchoring', 'claimAnchor', 'releaseAnchor', 'anchorPopup'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const viewOf = (element) => element.ownerDocument?.defaultView ?? globalThis;
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    if (!element || typeof element !== 'object') return;
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element?.[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    function round(value) {
    return Math.round(value * 100) / 100;
    }
    function pxOf(element, token, fallback) {
    const value = parseFloat(viewOf(element).getComputedStyle?.(element).getPropertyValue(token) ?? '');
    return Number.isFinite(value) ? value : fallback;
    }
    function computePlacement(rect, size, bounds, options = {}) {
    const wanted = options.side ?? 'bottom';
    const offset = options.offset ?? 0;
    const padding = options.padding ?? 0;
    const rtl = options.rtl === true;
    const flip = options.flip ?? 'room';
    const vertical = wanted === 'bottom' || wanted === 'top';
    const far = wanted === 'bottom' || (wanted === 'end' && !rtl) || (wanted === 'start' && rtl);
    const [from, to, low, high, extent] = vertical
    ? [rect.top, rect.bottom, bounds.top, bounds.bottom, size.height]
    : [rect.left, rect.right, bounds.left, bounds.right, size.width];
    const towardsFar = high - padding - to - offset;
    const towardsNear = from - offset - low - padding;
    const here = far ? towardsFar : towardsNear;
    const there = far ? towardsNear : towardsFar;
    const flipped = extent > here && ((flip === 'room' && there > here) || (flip === 'fits' && extent <= there));
    const goesFar = flipped ? !far : far;
    const room = Math.max(0, flipped ? there : here);
    const length = options.size === false ? extent : Math.min(extent, room);
    const main = goesFar ? to + offset : from - offset - length;
    const [crossFrom, crossTo, crossLow, crossHigh, crossExtent, inline] = vertical
    ? [rect.left, rect.right, bounds.left, bounds.right, size.width, true]
    : [rect.top, rect.bottom, bounds.top, bounds.bottom, size.height, false];
    const align = options.align ?? 'start';
    const reversed = inline && rtl;
    const startEdge = reversed ? crossTo - crossExtent : crossFrom;
    const endEdge = reversed ? crossFrom : crossTo - crossExtent;
    const fits = (at) => at >= crossLow + padding && at + crossExtent <= crossHigh - padding;
    let cross = (crossFrom + crossTo) / 2 - crossExtent / 2;
    if (align !== 'center') {
    const [first, second] = align === 'start' ? [startEdge, endEdge] : [endEdge, startEdge];
    cross = options.crossFlip && !fits(first) && fits(second) ? second : first;
    }
    if (options.shift !== false) cross = Math.max(crossLow + padding, Math.min(crossHigh - crossExtent - padding, cross));
    const arrowPadding = options.arrowPadding ?? 0;
    const arrow = Math.max(arrowPadding, Math.min(crossExtent - arrowPadding, (crossFrom + crossTo) / 2 - cross));
    const side = vertical ? (goesFar ? 'bottom' : 'top') : (goesFar === !rtl ? 'end' : 'start');
    return vertical
    ? { x: cross, y: main, side, flipped, room, length, arrow }
    : { x: main, y: cross, side, flipped, room, length, arrow };
    }
    function viewportBounds(element) {
    const doc = element.ownerDocument;
    const view = viewOf(element);
    return { left: 0, top: 0, right: doc.documentElement.clientWidth || view.innerWidth, bottom: doc.documentElement.clientHeight || view.innerHeight };
    }
    function clippedBounds(element) {
    const doc = element.ownerDocument;
    const view = viewOf(element);
    const bounds = viewportBounds(element);
    for (let node = element.parentElement; node && node !== doc.body && node !== doc.documentElement; node = node.parentElement) {
    if (view.getComputedStyle?.(node).overflowY === 'visible') continue;
    const box = node.getBoundingClientRect();
    bounds.top = Math.max(bounds.top, box.top);
    bounds.bottom = Math.min(bounds.bottom, box.bottom);
    }
    return bounds;
    }
    const probes = new WeakMap();
    function fixedOrigin(element) {
    const parent = element.parentNode;
    if (!parent) return { left: 0, top: 0 };
    let probe = probes.get(element);
    if (!probe) {
    probe = element.ownerDocument.createElement('div');
    probe.setAttribute('data-ds-keep', '');
    probe.setAttribute('aria-hidden', 'true');
    probe.style.cssText = 'position:fixed;left:0;top:0;width:0;height:0;margin:0;border:0;padding:0;transform:none;translate:none;scale:none;rotate:none;animation:none;transition:none;visibility:hidden;pointer-events:none';
    probes.set(element, probe);
    }
    if (probe.parentNode !== parent || probe.nextSibling !== element) parent.insertBefore(probe, element);
    const { left, top } = probe.getBoundingClientRect();
    return { left, top };
    }
    function dropProbe(element) {
    probes.get(element)?.remove();
    probes.delete(element);
    }
    const widthCapped = new WeakSet();
    function place(element, anchor, options = {}) {
    if (!anchor || typeof anchor.getBoundingClientRect !== 'function') return null;
    const view = viewOf(element);
    const style = element.style;
    const rect = anchor.getBoundingClientRect();
    const capped = options.size !== false;
    if (capped) {
    style.removeProperty('max-height');
    if (widthCapped.delete(element)) style.removeProperty('max-width');
    }
    const size = { width: element.offsetWidth, height: element.offsetHeight };
    const bounds = options.bounds ?? viewportBounds(element);
    const result = computePlacement(rect, size, bounds, { ...options, rtl: view.getComputedStyle?.(anchor).direction === 'rtl' });
    if (capped) {
    const inline = result.side === 'start' || result.side === 'end';
    if ((inline ? size.width : size.height) > result.room) {
    style.setProperty(inline ? 'max-width' : 'max-height', `${round(result.room)}px`);
    if (inline) widthCapped.add(element);
    }
    }
    if (options.x || options.y) {
    const origin = fixedOrigin(element);
    if (options.x) style.setProperty(options.x, `${round(result.x - origin.left)}px`);
    if (options.y) style.setProperty(options.y, `${round(result.y - origin.top)}px`);
    }
    if (result.side === 'bottom') element.removeAttribute('data-placement');
    else element.setAttribute('data-placement', result.side);
    return result;
    }
    function supportsAnchoring(view) {
    const proto = view.HTMLElement?.prototype ?? {};
    return typeof proto.showPopover === 'function' && typeof view.CSS?.supports === 'function'
    && view.CSS.supports('position-area: block-end') && 'anchorName' in view.document.documentElement.style;
    }
    const anchorClaims = new WeakMap();
    function claimAnchor(element, nextName) {
    const held = anchorClaims.get(element);
    if (held) {
    held.count += 1;
    return { name: held.name, mine: true };
    }
    const css = viewOf(element).getComputedStyle(element).getPropertyValue('anchor-name').trim();
    if (css && css !== 'none') return { name: css.split(',')[0].trim(), mine: false };
    const name = nextName();
    (element).style.setProperty('anchor-name', name);
    own(element, 'props', 'anchor-name');
    anchorClaims.set(element, { name, count: 1 });
    return { name, mine: true };
    }
    function releaseAnchor(element) {
    const held = element ? anchorClaims.get(element) : null;
    if (!held) return;
    held.count -= 1;
    if (held.count > 0) return;
    anchorClaims.delete(element);
    (element).style.removeProperty('anchor-name');
    disown(element, 'props', 'anchor-name');
    }
    function anchorPopup(element, anchor, options) {
    element.setAttribute('popover', 'manual');
    element.setAttribute('data-anchored', '');
    const { name, mine } = claimAnchor(anchor, options.nextName);
    const style = element.style;
    style.setProperty('position-anchor', name);
    try {
    element.showPopover();
    } catch {
    }
    style.setProperty('max-height', 'none');
    const height = element.offsetHeight;
    style.removeProperty('max-height');
    const offset = options.offset ?? 0;
    const { side } = computePlacement(anchor.getBoundingClientRect(), { width: 0, height }, viewportBounds(element), { offset, padding: offset });
    if (side === 'top') element.setAttribute('data-placement', 'top');
    else element.removeAttribute('data-placement');
    return mine;
    }

    return Object.freeze({
      round: round,
      pxOf: pxOf,
      computePlacement: computePlacement,
      viewportBounds: viewportBounds,
      clippedBounds: clippedBounds,
      fixedOrigin: fixedOrigin,
      dropProbe: dropProbe,
      place: place,
      supportsAnchoring: supportsAnchoring,
      claimAnchor: claimAnchor,
      releaseAnchor: releaseAnchor,
      anchorPopup: anchorPopup
    });
  });

  add('overlay', ['TOAST_MS', 'TOAST_MAX_MS', 'TOAST_LIMIT', 'TOOLTIP_DELAY_MS', 'TOOLTIP_SKIP_MS', 'TOOLTIP_HIDE_MS', 'DISMISS_DELAY_MS', 'DRAWER_QUERY', 'FOCUSABLE', 'canFocus', 'holdInert', 'lockScroll', 'unlockScroll', 'typeAhead', 'open', 'close', 'topmost', 'layerAt', 'savebar', 'confirm', 'alert', 'prompt', 'toastRegion', 'announce', 'toast', 'closeToasts', 'tooltips', 'internals'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, runtime = DS.core.runtime;
    const motionDuration = DS.motion.motionDuration, originFrom = DS.motion.originFrom, settled = DS.motion.settled, transition = DS.motion.transition;
    const scrollEdges = DS.dom.scrollEdges, watchScrolled = DS.dom.watchScrolled;
    const anchorPopup = DS.position.anchorPopup, clippedBounds = DS.position.clippedBounds, dropProbe = DS.position.dropProbe, place = DS.position.place, pxOf = DS.position.pxOf, releaseAnchor = DS.position.releaseAnchor, round = DS.position.round, supportsAnchoring = DS.position.supportsAnchoring;
    const TOAST_MS = 4000;
    const TOAST_MAX_MS = 10000;
    const TOAST_LIMIT = 3;
    const TOOLTIP_DELAY_MS = 500;
    const TOOLTIP_SKIP_MS = 300;
    const TOOLTIP_HIDE_MS = 100;
    const DISMISS_DELAY_MS = 300;
    const DRAWER_QUERY = '(max-width: 900px)';
    const FOCUSABLE = 'a[href], area[href], button, input:not([type="hidden"]), select, textarea, summary, iframe, audio[controls], video[controls], [contenteditable]:not([contenteditable="false"]), [tabindex]';
    const LANDMARK = '.ds-view[tabindex="-1"], main[tabindex="-1"], [role="main"][tabindex="-1"]';
    const MODAL = new Set(['dialog', 'sheet', 'drawer']);
    const overlays = new WeakMap();
    const stack = ([]);
    function activeIn(node) {
    const root = node?.getRootNode?.();
    return (root?.nodeType === 11 ? root.activeElement : null) ?? node?.ownerDocument?.activeElement ?? null;
    }
    function inside(node, event) {
    if (!node) return false;
    const path = event.composedPath?.();
    return path?.length ? path.includes(node) : node.contains( (event.target));
    }
    const SHARED = Symbol.for('ds.overlay');
    const sharedRecords = runtime.state('overlay', () => new WeakMap());
    function sharedOf(doc) {
    let record = sharedRecords.get(doc);
    if (!record) {
    record = doc[SHARED] ?? {};
    sharedRecords.set(doc, record);
    doc[SHARED] = record;
    }
    record.order ??= [];
    record.lock ??= { count: 0, saved: null };
    record.seq ??= 0;
    record.tipRoots ??= new WeakMap();
    record.inertHolds ??= new WeakMap();
    return record;
    }
    function enterOrder(element) {
    const order = sharedOf(element.ownerDocument).order;
    leaveOrder(element);
    order.push({
    element,
    open: () => element.isConnected && overlays.get(element)?.phase === 'open',
    opener: () => overlays.get(element)?.opener ?? null,
    escape(event) {
    if (containedDialog(element) && !inside(containerOf(element), event)) return undefined;
    if (!escapeReaches(element, event)) return false;
    event.preventDefault();
    closeFrom(element, 'escape');
    return true;
    },
    });
    }
    const NON_MODAL = '.ds-dialog__box--modeless, .ds-sheet__panel--push, .ds-sheet__panel--inspector';
    function escapeReaches(element, event) {
    if (!element.matches?.(NON_MODAL)) return true;
    const target = (event.composedPath?.()[0] ?? event.target);
    const doc = element.ownerDocument;
    if (!target || target === doc || target === doc.body || target === doc.documentElement) return true;
    const docked = element.matches('.ds-sheet__panel--push, .ds-sheet__panel--inspector') ? element.closest('.ds-sheet__layout') : null;
    return inside(docked ?? element, event) || inside(overlays.get(element)?.opener ?? null, event);
    }
    function leaveOrder(element) {
    const order = element.ownerDocument?.[SHARED]?.order;
    const index = order ? order.findIndex((entry) => entry.element === element) : -1;
    if (index >= 0) order.splice(index, 1);
    }
    const managedInert = (new Set());
    const tooltipControllers = new Set();
    const neverInert = new WeakSet();
    const shownSavebars = new Set();
    const parts = {};
    const pageParts = runtime.state('overlay-parts', () => ({}));
    function partFor(name, fn) {
    const found = parts[name] ?? pageParts[name];
    if (found) return found;
    throw new Error(`Z-Dash: DS.overlay.${fn}() needs the ${name} module: import js/${name}.js next to js/overlay.js, or keep ${name} in the bundle.`);
    }
    const partsWarned = new Set();
    function warnMissingPart(element, kind) {
    if (parts[kind] || partsWarned.has(kind)) return;
    partsWarned.add(kind);
    viewOf(element).console?.warn(`Z-Dash: DS.overlay.open() on a ${kind} needs the ${kind} module: import js/${kind}.js next to js/overlay.js, or keep ${kind} in the bundle.`);
    }
    const listening = new WeakSet();
    const wired = new WeakSet();
    const watchedDrawers = new WeakSet();
    let warned = false;
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    if (!element || typeof element !== 'object') return;
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element?.[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const PANEL_STATE = {
    attrs: ['data-state', 'hidden', 'open', 'popover', 'data-anchored', 'data-placement'],
    classes: ['ds-dialog--closing', 'ds-sheet--closing', 'ds-dialog--nested', 'ds-dialog--hidden', 'ds-sheet--hidden'],
    props: ['--ds-menu-x', '--ds-menu-y', 'max-height', 'position-anchor'],
    };
    function claim(state, node, kind, names) {
    if (!node) return;
    for (const name of names) {
    own(node, kind, name);
    state.claims.push([node, kind, name]);
    }
    }
    function releaseClaims(state) {
    for (const [node, kind, name] of state.claims) disown(node, kind, name);
    state.claims = [];
    }
    function stateOf(element) {
    let state = overlays.get(element);
    if (!state) {
    state = { gen: 0, phase: 'closed', kind: kindOf(element), opener: null, expander: null, inerted: [], locked: false, lockedDoc: null, hiddenClass: '', opening: null, closing: null, anchor: null, home: null, app: null, container: null, claims: [], openerId: '', openerRoot: null, returnTo: null, shownAt: 0, pressed: null, cancelled: false, edges: null };
    overlays.set(element, state);
    }
    return state;
    }
    function kindOf(element) {
    if (element.matches('.ds-drawer')) return 'drawer';
    if (element.matches('.ds-dialog')) return 'dialog';
    if (element.matches('.ds-sheet')) return 'sheet';
    if (element.matches('.ds-menu')) return 'menu';
    if (element.matches('.ds-combo__popup')) return 'combo';
    return 'popup';
    }
    function boxOf(element, kind) {
    if (kind === 'dialog') return element.querySelector('.ds-dialog__box') ?? element;
    if (kind === 'sheet') return element.querySelector('.ds-sheet__panel') ?? element;
    return element;
    }
    const isNative = (element) => element.tagName === 'DIALOG' && typeof element.showModal === 'function';
    const viewOf = (element) => element.ownerDocument?.defaultView ?? globalThis;
    function flushStyle(element) {
    const view = viewOf(element);
    if (typeof view.getComputedStyle === 'function') void view.getComputedStyle(element).opacity;
    }
    function canFocus(node) {
    if (!node || node.isConnected === false || typeof node.focus !== 'function') return false;
    if (node.disabled || node.closest?.('[inert], [hidden]')) return false;
    return typeof node.getClientRects !== 'function' || node.getClientRects().length > 0;
    }
    function firstFocusable(root) {
    for (const node of root.querySelectorAll(FOCUSABLE)) {
    if (node.getAttribute('tabindex') === '-1' || node.getAttribute('role') === 'separator') continue;
    if (canFocus(node)) return node;
    }
    return null;
    }
    function returnTarget(element, state) {
    const takes = (node) => canFocus(node) && (typeof node.matches !== 'function' || node.matches(FOCUSABLE));
    let opener = state.opener;
    for (let depth = 0; opener && depth < 4; depth += 1) {
    if (takes(opener) && !element.contains(opener)) return opener;
    const host = [...stack, ...hostsOf(opener)].find((node) => node !== element && node.contains(opener));
    opener = host ? overlays.get(host)?.opener ?? null : null;
    }
    const outside = (node) => takes(node) && !element.contains(node);
    if (typeof state.returnTo === 'function') {
    try {
    const target = state.returnTo();
    if (outside(target)) return target;
    } catch (error) {
    report(element, error);
    }
    }
    const twin = state.openerId ? state.openerRoot?.getElementById?.(state.openerId) : null;
    if (twin && twin !== state.opener && outside(twin)) return twin;
    const doc = element.ownerDocument;
    const near = element.parentElement?.closest('[tabindex="-1"]');
    return [near, doc.querySelector(LANDMARK)].find(outside) ?? null;
    }
    function report(element, error) {
    const view = viewOf(element);
    if (typeof view.reportError === 'function') view.reportError(error);
    else view.console?.error(error);
    }
    function defaultOpener(element) {
    const doc = element.ownerDocument;
    const active = activeIn(element);
    if (active && active !== doc.body && !element.contains(active)) return active;
    if (active && active !== doc.body) return null;
    const event = (viewOf(element)).event;
    const origin = event?.composedPath?.()[0] ?? event?.target;
    const control = origin?.closest?.(OPENER_CONTROL);
    return control && canFocus(control) && !element.contains(control) ? control : null;
    }
    const OPENER_CONTROL = 'button, a[href], input:not([type="hidden"]), select, textarea, summary, [role="button"], [tabindex]:not([tabindex="-1"])';
    function hostsOf(node) {
    const hosts = [];
    for (let el = node?.parentElement; el; el = el.parentElement) if (overlays.has(el)) hosts.push(el);
    return hosts;
    }
    const TEXT_ENTRY = 'input:not([type="button"], [type="submit"], [type="reset"], [type="checkbox"], [type="radio"], [type="range"], [type="color"], [type="file"], [type="image"]), textarea, select, [contenteditable]:not([contenteditable="false"])';
    const MODIFIER_KEYS = new Set(['Shift', 'Control', 'Alt', 'Meta', 'AltGraph', 'CapsLock']);
    const INPUT = Symbol.for('ds.input');
    function watchModality(doc) {
    if (typeof doc?.addEventListener !== 'function' || doc[INPUT]) return;
    const record = (doc[INPUT] = { last: '' });
    for (const type of ['pointerdown', 'mousedown']) doc.addEventListener(type, () => { record.last = 'pointer'; }, { capture: true, passive: true });
    doc.addEventListener('keydown', ( event) => {
    if (!MODIFIER_KEYS.has(event.key)) record.last = 'key';
    }, true);
    }
    function keepFocusVisibility(element, run) {
    const before = activeIn(element);
    const body = element.ownerDocument.body;
    const was = Boolean(before) && before !== body && before.matches(':focus-visible');
    const event = (viewOf(element)).event;
    const type = event?.type ?? '';
    const key = /^key/.test(type) || (type === 'click' && event.detail === 0);
    const press = !key && /^(pointer|mouse|touch|click$)/.test(type);
    const pointer = press || (!key && element.ownerDocument[INPUT]?.last === 'pointer');
    run();
    const now = activeIn(element);
    if (was || !pointer || !now || now === before || now === body || now.matches(TEXT_ENTRY) || !now.matches(':focus-visible')) return;
    now.blur();
    now.focus({ preventScroll: true, focusVisible: false });
    }
    function focusInto(element, state, box) {
    const autofocus = element.querySelector('[autofocus]');
    if (canFocus(autofocus)) {
    autofocus.focus({ preventScroll: true });
    return;
    }
    if (state.kind === 'combo') return;
    if (state.kind === 'menu') {
    parts.menu?.focusInto(element);
    return;
    }
    if (state.kind === 'drawer') {
    const current = element.querySelector('[aria-current="page"]');
    if (canFocus(current)) {
    current.focus({ preventScroll: true });
    return;
    }
    }
    if (state.kind === 'drawer' && element.hasAttribute('tabindex')) {
    element.focus({ preventScroll: true });
    return;
    }
    if (state.kind === 'popup') return;
    const target = firstFocusable(box);
    if (target) target.focus({ preventScroll: true });
    else {
    if (!box.hasAttribute('tabindex')) box.setAttribute('tabindex', '-1');
    box.focus({ preventScroll: true });
    }
    }
    function inertOutside(element, stop = element.ownerDocument.body) {
    const taken = [];
    const body = element.ownerDocument.body;
    const holds = sharedOf(element.ownerDocument).inertHolds;
    for (let node = element; node && node !== stop && node !== body;) {
    const parent = node.parentNode;
    if (!parent || parent.nodeType === 9) break;
    for (const sibling of (parent).children) {
    if (sibling === node || sibling.matches('.ds-scrim, .ds-toasts') || neverInert.has(sibling)) continue;
    if (sibling.inert && !managedInert.has(sibling) && !holds.has(sibling)) continue;
    taken.push(sibling);
    }
    node = parent.nodeType === 11 ? (parent).host : (parent);
    }
    return taken;
    }
    function composedContains(node, other) {
    for (let at = other; at; at = at.parentNode ?? (at).host ?? null) {
    if (at === node) return true;
    }
    return false;
    }
    function refreshTraps(targets) {
    let grew = false;
    for (const element of stack) {
    const state = overlays.get(element);
    if (!state?.inerted.length || state.phase !== 'open' || !element.isConnected) continue;
    if (!targets.some((target) => composedContains(target, element))) continue;
    const stop = containedDialog(element) ? containerOf(element) : undefined;
    const known = new Set(state.inerted);
    for (const node of inertOutside(element, stop)) {
    if (known.has(node)) continue;
    state.inerted.push(node);
    grew = true;
    }
    }
    if (grew) syncInert();
    }
    function syncInert() {
    const wanted = new Set();
    stack.forEach((element, index) => {
    const above = stack.slice(index + 1);
    for (const node of overlays.get(element)?.inerted ?? []) {
    if (node.isConnected && !above.some((other) => composedContains(node, other))) wanted.add(node);
    }
    });
    const closingBoxes = new Set(stack.filter((element) => overlays.get(element)?.phase === 'closing')
    .map((element) => boxOf(element, overlays.get(element)?.kind ?? '')));
    for (const node of [...managedInert]) {
    if (wanted.has(node)) continue;
    managedInert.delete(node);
    disown(node, 'attrs', 'inert');
    if (!closingBoxes.has(node) && !sharedOf(node.ownerDocument).inertHolds.has(node)) node.inert = false;
    }
    for (const node of wanted) {
    if (!managedInert.has(node)) own(node, 'attrs', 'inert');
    managedInert.add(node);
    node.inert = true;
    }
    }
    const containedDialog = (element) => element.matches('.ds-dialog--contained');
    function containerOf(element) {
    const doc = element.ownerDocument;
    const view = viewOf(element);
    for (let node = element.parentElement; node && node !== doc.body && node !== doc.documentElement; node = node.parentElement) {
    if ((view.getComputedStyle?.(node).position ?? 'static') !== 'static') return node;
    }
    return element.closest('.ds-theme') ?? doc.body;
    }
    function restoreInert(state) {
    state.inerted = [];
    syncInert();
    }
    function holdInert(elements) {
    const nodes = [...new Set(elements)].filter((node) => node?.ownerDocument);
    for (const node of nodes) {
    const holds = sharedOf(node.ownerDocument).inertHolds;
    const held = holds.get(node);
    if (held) {
    held.count += 1;
    continue;
    }
    holds.set(node, { count: 1, pageOwn: node.inert === true && !managedInert.has(node) });
    own(node, 'attrs', 'inert');
    node.inert = true;
    }
    let done = false;
    return {
    destroy() {
    if (done) return;
    done = true;
    for (const node of nodes) {
    const holds = sharedOf(node.ownerDocument).inertHolds;
    const held = holds.get(node);
    held.count -= 1;
    if (held.count) continue;
    holds.delete(node);
    disown(node, 'attrs', 'inert');
    if (!held.pageOwn && !managedInert.has(node)) node.inert = false;
    }
    },
    };
    }
    function supportsGutter(view) {
    return typeof view.CSS?.supports === 'function' && view.CSS.supports('scrollbar-gutter: stable');
    }
    function lockScroll(doc = globalThis.document) {
    if (!doc?.body) return;
    const lock = sharedOf(doc).lock;
    lock.count += 1;
    if (lock.count !== 1) return;
    const root = doc.documentElement;
    const body = doc.body;
    const view = doc.defaultView ?? globalThis;
    const before = (view.innerWidth || 0) - (root.clientWidth || 0);
    lock.saved = { gutter: root.style.scrollbarGutter ?? '', padding: body.style.paddingRight ?? '' };
    body.classList.add('ds-no-scroll');
    const gap = before - ((view.innerWidth || 0) - (root.clientWidth || 0));
    if (!(before > 0) || !(gap > 0)) return;
    if (supportsGutter(view)) root.style.scrollbarGutter = 'stable';
    else {
    const padding = parseFloat(view.getComputedStyle?.(body).paddingRight ?? '') || 0;
    body.style.paddingRight = `${padding + gap}px`;
    }
    }
    function unlockScroll(doc = globalThis.document) {
    const lock = doc?.body ? doc[SHARED]?.lock : null;
    if (!lock || lock.count === 0) return;
    lock.count -= 1;
    if (lock.count) return;
    doc.body.classList.remove('ds-no-scroll');
    if (lock.saved) {
    doc.documentElement.style.scrollbarGutter = lock.saved.gutter;
    doc.body.style.paddingRight = lock.saved.padding;
    }
    lock.saved = null;
    }
    function topOpen() {
    pruneDetached();
    for (let i = stack.length - 1; i >= 0; i -= 1) {
    if (overlays.get(stack[i])?.phase === 'open') return stack[i];
    }
    return null;
    }
    function listen(doc) {
    if (listening.has(doc)) return;
    listening.add(doc);
    watchModality(doc);
    doc.addEventListener('keydown', ( event) => {
    if (event.key !== 'Escape' || event.isComposing || event.defaultPrevented) return;
    for (const tip of tooltipControllers) {
    if (!tip.escape()) continue;
    event.preventDefault();
    return;
    }
    pruneDetached();
    const order = doc[SHARED]?.order ?? [];
    for (let i = order.length - 1; i >= 0; i -= 1) {
    if (!order[i].open()) continue;
    if (clearSearch(order[i].element, event)) return;
    if (order[i].escape(event) === false) continue;
    return;
    }
    });
    doc.addEventListener('pointerdown', ( event) => {
    pruneDetached();
    for (const element of [...stack].reverse()) {
    const state = overlays.get(element);
    if (state?.kind !== 'menu' || state.phase !== 'open') continue;
    if (inside(element, event) || inside(state.opener, event)) continue;
    closeFrom(element, 'outside');
    }
    }, true);
    }
    function clearSearch(layer, event) {
    const target = (event.composedPath?.()[0] ?? event.target);
    if (!target || target.localName !== 'input' || target.type !== 'search' || !target.value) return false;
    if (!composedContains(layer, target) || target.readOnly || target.disabled) return false;
    event.preventDefault();
    target.value = '';
    const view = viewOf(target);
    target.dispatchEvent(new view.Event('input', { bubbles: true, composed: true }));
    return true;
    }
    const CLOSERS = '[data-ds-close=""]';
    const USER_CLOSE = new Set(['escape', 'backdrop', 'close']);
    function dismissable(element, state, part, event) {
    if (event.detail === 0) return true;
    if (event.detail > 1 || !state.pressed || !composedContains(part, state.pressed)) return false;
    return clock() - state.shownAt >= motionDuration(element, '--ds-delay-dismiss');
    }
    function wire(element, state) {
    if (wired.has(element)) return;
    wired.add(element);
    const holdFocus = ( event) => {
    const phase = overlays.get(element)?.phase;
    if (!phase || phase === 'closed') return;
    const target = (event.target);
    if (phase === 'closing' || target.closest?.('.ds-dialog__backdrop, .ds-sheet__backdrop, .ds-scrim')) event.preventDefault();
    };
    element.addEventListener('mousedown', holdFocus, true);
    const pressAt = ( event) => {
    const own = overlays.get(element);
    if (own) own.pressed = (event.composedPath?.()[0] ?? event.target);
    };
    element.addEventListener('pointerdown', pressAt, true);
    element.addEventListener('click', ( event) => {
    const target = (event.target);
    const closer = target.closest?.(CLOSERS);
    if (!closer || !element.contains(closer)) return;
    if (hostsOf(closer)[0] !== element && closer !== element) return;
    const own = overlays.get(element);
    if (own?.phase !== 'open') return;
    if (event.detail > 1) return;
    const backdrop = closer.matches('.ds-dialog__backdrop, .ds-sheet__backdrop');
    if (backdrop && !dismissable(element, own, closer, event)) return;
    closeFrom(element, backdrop ? 'backdrop' : 'close');
    });
    if (state.kind === 'menu') parts.menu?.wire(element);
    if (state.kind === 'dialog') parts.dialog?.wire(element);
    if (state.kind === 'drawer') {
    const scrim = element.parentElement?.querySelector(':scope > .ds-scrim');
    scrim?.addEventListener('click', ( event) => {
    const own = overlays.get(element);
    if (own?.phase === 'open' && dismissable(element, own, scrim, event)) closeFrom(element, 'backdrop');
    });
    scrim?.addEventListener('mousedown', holdFocus, true);
    scrim?.addEventListener('pointerdown', pressAt, true);
    }
    if (isNative(element)) {
    element.addEventListener('cancel', (event) => {
    const own = overlays.get(element);
    if (own && !event.cancelable) own.cancelled = true;
    event.preventDefault();
    if (own?.phase === 'open') closeFrom(element, 'escape');
    });
    element.addEventListener('close', () => {
    const own = overlays.get(element);
    if (own?.phase !== 'open' || element.open) return;
    const reason = own.cancelled ? 'escape' : 'api';
    own.cancelled = false;
    closeFrom(element, 'native', reason);
    });
    }
    }
    function watchBodies(element) {
    for (const body of element.querySelectorAll?.('.ds-dialog__body, .ds-sheet__body') ?? []) {
    if (body.closest('.ds-dialog, .ds-sheet') !== element) continue;
    const handle = watchScrolled(body);
    if (!body.querySelector(':scope > [data-ds-sentinel]')) handle.update();
    }
    }
    function arrowTarget(items, event) {
    if (!items.length || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return null;
    const current = items.indexOf( (event.target)?.closest?.('.ds-menu__item'));
    if (event.key === 'Home') return items[0];
    if (event.key === 'End') return items.at(-1);
    if (event.key === 'ArrowDown') return items[(current + 1) % items.length];
    return items[current < 0 ? items.length - 1 : (current - 1 + items.length) % items.length];
    }
    const menuItemLabel = (item) => (item.querySelector('.ds-menu__label') ?? item).textContent.replace(/\s+/g, ' ').trim().toLowerCase();
    function typeAhead(items, current, typed, label = menuItemLabel) {
    const repeated = typed.length > 1 && [...typed].every((char) => char === typed[0]);
    const start = items.indexOf(current);
    const from = typed.length === 1 || repeated ? start + 1 : Math.max(0, start);
    for (const text of repeated ? [typed, typed[0]] : [typed]) {
    for (let step = 0; step < items.length; step += 1) {
    const item = items[(from + step) % items.length];
    if (label(item).startsWith(text)) return item;
    }
    }
    return null;
    }
    function popupAnchoring(view, kind) {
    if (!supportsAnchoring(view)) return false;
    return kind !== 'combo' || view.CSS.supports('width: anchor-size(width)');
    }
    function popupAnchorOf(element, state) {
    const anchor = state.kind === 'combo' ? element.closest('.ds-combo') ?? state.opener : state.opener;
    return anchor && typeof anchor.getBoundingClientRect === 'function' ? anchor : null;
    }
    function placePopup(element, state) {
    const anchor = popupAnchorOf(element, state);
    if (!anchor) return null;
    const view = viewOf(element);
    if (popupAnchoring(view, state.kind)) {
    releaseAnchor(state.anchor);
    state.anchor = null;
    const mine = anchorPopup( (element), anchor, { nextName: () => nextAnchorName(element.ownerDocument), offset: pxOf(element, '--ds-space-1', 4) });
    if (mine) state.anchor = (anchor);
    } else if (state.kind === 'menu') placeMenu(element, anchor);
    else placeList(element, anchor);
    return anchor;
    }
    function nextAnchorName(doc) {
    return `--ds-anchor-${sharedOf(doc).seq += 1}`;
    }
    function placeMenu(menu, anchor) {
    place(menu, anchor, { offset: pxOf(menu, '--ds-space-1', 4), padding: pxOf(menu, '--ds-space-2', 8), crossFlip: true, x: '--ds-menu-x', y: '--ds-menu-y' });
    }
    function placeList(list, anchor) {
    const gap = pxOf(list, '--ds-space-1', 4);
    place(list, anchor, { offset: gap, padding: gap, shift: false, bounds: clippedBounds(list) });
    }
    function releasePopup(element, state) {
    dropProbe(element);
    if (element.hasAttribute('popover')) {
    try {
    if (element.matches(':popover-open')) (element).hidePopover();
    } catch {
    }
    element.removeAttribute('popover');
    }
    element.removeAttribute('data-anchored');
    element.removeAttribute('data-placement');
    (element).style.removeProperty('position-anchor');
    (element).style.removeProperty('max-height');
    releaseAnchor(state.anchor);
    state.anchor = null;
    unwatchEdges(state);
    }
    function watchEdges(element, state) {
    const scroller = state.kind === 'combo' ? element.querySelector('.ds-combo__list') ?? element : element;
    if (state.edges?.scroller === scroller) {
    state.edges.handle.update();
    return;
    }
    unwatchEdges(state);
    if (scroller.hasAttribute('data-ds-edges')) return;
    state.edges = { scroller, handle: scrollEdges(scroller, { axis: 'block' }) };
    }
    function unwatchEdges(state) {
    state.edges?.handle.destroy();
    state.edges = null;
    }
    function nativeHostFor(element) {
    for (let i = stack.length - 1; i >= 0; i -= 1) {
    const other = stack[i];
    const state = overlays.get(other);
    if (other === element || state?.phase !== 'open' || !MODAL.has(state.kind) || !isNative(other) || !other.open) continue;
    return other.contains(element) ? null : other;
    }
    return null;
    }
    function adopt(element, state) {
    if (state.home || !['menu', 'popup', 'dialog', 'sheet'].includes(state.kind)) return;
    if (isNative(element) && element.open) return;
    const host = nativeHostFor(element);
    if (!host || !element.parentNode) return;
    state.home = { parent: element.parentNode, next: element.nextSibling, host, kept: keepAway(element) };
    host.append(element);
    }
    function keepAway(node) {
    if (!node || node.hasAttribute('data-ds-keep')) return false;
    node.setAttribute('data-ds-keep', '');
    return true;
    }
    function keepHome(node, kept) {
    if (kept) node?.removeAttribute('data-ds-keep');
    }
    function giveBack(element, state) {
    const home = state.home;
    state.home = null;
    if (home) keepHome(element, home.kept);
    if (!home || element.parentNode === home.parent) return;
    home.parent.insertBefore(element, home.next && home.next.parentNode === home.parent ? home.next : null);
    }
    function drawerQuery(drawer) {
    const view = viewOf(drawer);
    return typeof view.matchMedia === 'function' ? view.matchMedia(DRAWER_QUERY) : null;
    }
    const drawerCrossings = new WeakMap();
    function crossDrawer(drawer, query) {
    const view = viewOf(drawer);
    const run = (drawerCrossings.get(drawer) ?? 0) + 1;
    drawerCrossings.set(drawer, run);
    const frame = (callback) => (typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(callback) : setTimeout(callback));
    drawer.classList.add('ds-drawer--instant');
    if (!query.matches) {
    const state = overlays.get(drawer);
    if (state && state.phase !== 'closed') closeNow(drawer, state);
    }
    frame(() => frame(() => {
    if (drawerCrossings.get(drawer) === run) drawer.classList.remove('ds-drawer--instant');
    }));
    }
    const drawerWatches = new WeakMap();
    function watchDrawer(drawer, query) {
    if (!query || watchedDrawers.has(drawer)) return;
    watchedDrawers.add(drawer);
    const drawers = drawerWatchOf(viewOf(drawer), query);
    for (const ref of [...drawers]) if (!ref.deref()) drawers.delete(ref);
    drawers.add(new WeakRef(drawer));
    }
    function drawerWatchOf(view, query) {
    const known = drawerWatches.get(view);
    if (known) return known.drawers;
    const watch = { query, drawers: (new Set()) };
    drawerWatches.set(view, watch);
    const onChange = () => {
    for (const ref of [...watch.drawers]) {
    const drawer = ref.deref();
    if (!drawer) watch.drawers.delete(ref);
    else if (drawer.isConnected) crossDrawer(drawer, watch.query);
    }
    };
    query.addEventListener?.('change', onChange);
    return watch.drawers;
    }
    if (typeof globalThis.matchMedia === 'function' && typeof globalThis.document?.querySelectorAll === 'function') {
    const query = globalThis.matchMedia(DRAWER_QUERY);
    const onChange = () => {
    for (const drawer of globalThis.document.querySelectorAll('.ds-app > .ds-drawer')) {
    if (!watchedDrawers.has(drawer)) crossDrawer(drawer, query);
    }
    };
    query.addEventListener?.('change', onChange);
    }
    function closeNow(drawer, state) {
    const wasOpen = state.phase === 'open';
    state.gen += 1;
    state.closing = null;
    state.opening = null;
    state.phase = 'closed';
    (state.app ?? drawer.closest('.ds-app'))?.removeAttribute('data-drawer-open');
    state.expander?.setAttribute('aria-expanded', 'false');
    restoreInert(state);
    drawer.inert = false;
    finishClose(drawer, state);
    if (wasOpen) announceClose(drawer, 'breakpoint');
    }
    function finishClose(element, state) {
    const index = stack.indexOf(element);
    if (index >= 0) stack.splice(index, 1);
    leaveOrder(element);
    syncInert();
    if (state.locked) {
    state.locked = false;
    unlockScroll(state.lockedDoc ?? undefined);
    state.lockedDoc = null;
    }
    releaseClaims(state);
    state.app = null;
    unwatchRemovals(element.ownerDocument);
    }
    const removals = new WeakMap();
    function watchRemovals(doc) {
    let watch = removals.get(doc);
    if (!watch) {
    const view = doc.defaultView ?? globalThis;
    if (typeof view.MutationObserver !== 'function') return;
    watch = { observer: new view.MutationObserver(pruneDetached), on: false };
    removals.set(doc, watch);
    }
    if (watch.on || !doc.documentElement) return;
    watch.on = true;
    watch.observer.observe(doc.documentElement, { childList: true, subtree: true });
    }
    function unwatchRemovals(doc) {
    const watch = removals.get(doc);
    if (!watch?.on || stack.some((element) => element.ownerDocument === doc)) return;
    watch.on = false;
    watch.observer.disconnect();
    }
    function pruneDetached(records) {
    if (stack.some((element) => !element.isConnected)) dropDetached();
    const grown = records?.filter((record) => record.addedNodes.length).map((record) => record.target) ?? [];
    if (grown.length) refreshTraps(grown);
    }
    function dropDetached() {
    for (const element of stack.filter((item) => !item.isConnected).reverse()) {
    if (element.isConnected) continue;
    const state = overlays.get(element);
    if (!state) {
    stack.splice(stack.indexOf(element), 1);
    leaveOrder(element);
    continue;
    }
    const kind = state.kind;
    const wasOpen = state.phase === 'open';
    dropProbe(element);
    state.gen += 1;
    state.phase = 'closed';
    state.closing = null;
    state.opening = null;
    state.container = null;
    const home = state.home;
    state.home = null;
    state.inerted = [];
    state.expander?.setAttribute('aria-expanded', 'false');
    if (kind === 'drawer') (state.app ?? element.closest('.ds-app'))?.removeAttribute('data-drawer-open');
    else {
    if (isNative(element) && element.open) element.close();
    element.classList.remove('ds-dialog--closing', 'ds-sheet--closing');
    if (!isNative(element)) {
    if (state.hiddenClass) element.classList.add(state.hiddenClass);
    else (element).hidden = true;
    }
    if (kind === 'menu' || kind === 'combo') releasePopup(element, state);
    element.removeAttribute('data-state');
    }
    if (kind === 'dialog' || kind === 'sheet') parts.toast?.release(element);
    boxOf(element, kind).inert = false;
    element.classList.remove('ds-dialog--nested');
    if (home && home.host?.contains(element) && home.parent.isConnected) {
    state.home = home;
    giveBack(element, state);
    } else if (home) keepHome(element, home.kept);
    finishClose(element, state);
    if (wasOpen) announceClose(element, 'removed');
    }
    }
    function open(element, options = {}) {
    if (!element?.isConnected) return Promise.resolve();
    if (options.container) {
    const transform = containerOpen(element, options);
    if (transform) return transform;
    }
    return show(element, options, false);
    }
    const UNNAMED_IN_CONTAINER = '.ds-view, .ds-panel, .ds-drawer';
    function containerOpen(element, options) {
    const doc = element.ownerDocument;
    const state = stateOf(element);
    if (state.kind !== 'dialog' && state.kind !== 'sheet') return null;
    if (state.phase !== 'closed' || state.container) return state.container?.done ?? null;
    const viewTransitions = typeof doc.startViewTransition === 'function' && doc.documentElement.classList.contains('ds-theme');
    if (!viewTransitions) return null;
    const opener = options.opener ?? defaultOpener(element);
    if (!opener?.isConnected || typeof opener.getBoundingClientRect !== 'function') return null;
    const lifted = new Map();
    for (const part of [...doc.querySelectorAll(UNNAMED_IN_CONTAINER), ...element.querySelectorAll('.ds-sheet__body')]) {
    lifted.set(part, part.style.getPropertyValue('view-transition-name'));
    part.style.setProperty('view-transition-name', 'none');
    }
    const restore = () => {
    for (const [part, before] of lifted) {
    if (part.style.getPropertyValue('view-transition-name') !== 'none') continue;
    if (before) part.style.setProperty('view-transition-name', before);
    else part.style.removeProperty('view-transition-name');
    }
    };
    const pending = { done: (null) };
    state.container = pending;
    let entrance = Promise.resolve();
    const update = () => {
    const current = state.container === pending;
    if (current) state.container = null;
    if (!current || !element.isConnected) return null;
    const transform = doc.documentElement.getAttribute('data-ds-vt') === 'container';
    entrance = show(element, { ...options, opener }, transform);
    return transform ? boxOf(element, state.kind) : null;
    };
    pending.done = transition(update, { type: 'container', from: opener }).then(
    () => { restore(); return entrance; },
    (error) => { restore(); throw error; },
    );
    return pending.done;
    }
    function remember(state, options) {
    const opener = state.opener;
    state.openerId = typeof opener?.id === 'string' ? opener.id : '';
    state.openerRoot = state.openerId ? opener.getRootNode?.() ?? opener.ownerDocument : null;
    state.returnTo = typeof options.returnTo === 'function' ? options.returnTo : null;
    }
    function show(element, options, transform) {
    const doc = element.ownerDocument;
    const state = stateOf(element);
    state.container = null;
    const kind = state.kind;
    const box = boxOf(element, kind);
    const app = kind === 'drawer' ? element.closest('.ds-app') : null;
    if (kind === 'drawer') {
    const query = drawerQuery(element);
    watchDrawer(element, query);
    if (query && !query.matches) return Promise.resolve();
    }
    if (!warned && !element.closest('.ds-theme')) {
    warned = true;
    viewOf(element).console?.warn('Z-Dash: DS.overlay.open() on an element outside .ds-theme. Overlays must sit inside the .ds-theme element, or they get neither the tokens nor the motion.');
    }
    if (kind === 'menu' || kind === 'dialog') warnMissingPart(element, kind);
    for (const tip of tooltipControllers) tip.hide();
    if (state.phase === 'open') return state.opening ?? Promise.resolve();
    pruneDetached();
    listen(doc);
    wire(element, state);
    if (kind === 'dialog' || kind === 'sheet') watchBodies(element);
    let reopening = state.phase === 'closing';
    if (reopening && (kind === 'menu' || kind === 'combo' || kind === 'popup') && options.opener && options.opener !== state.opener) {
    if (kind !== 'popup') releasePopup(element, state);
    reopening = false;
    }
    const gen = ++state.gen;
    state.closing = null;
    state.phase = 'open';
    if (!reopening) {
    const focused = defaultOpener(element);
    const menuButton = app?.querySelector('.ds-topbar__menu') ?? null;
    state.opener = options.opener ?? (kind === 'drawer' ? menuButton ?? focused : focused);
    const expands = Boolean(state.opener?.hasAttribute?.('aria-expanded'));
    state.expander = kind === 'drawer' ? menuButton ?? state.opener : MODAL.has(kind) ? (expands ? state.opener : null) : state.opener;
    remember(state, options);
    releaseClaims(state);
    for (const key of (['attrs', 'classes', 'props'])) claim(state, element, key, PANEL_STATE[key]);
    claim(state, box, 'attrs', ['inert']);
    claim(state, state.expander, 'attrs', ['aria-expanded']);
    claim(state, app, 'attrs', ['data-drawer-open']);
    state.app = app;
    } else if (options.opener) {
    state.opener = options.opener;
    remember(state, options);
    }
    state.shownAt = clock();
    state.pressed = null;
    const contained = kind === 'dialog' && containedDialog(element);
    const index = stack.indexOf(element);
    if (index >= 0) stack.splice(index, 1);
    if (MODAL.has(kind)) {
    if (!state.locked && !contained) {
    state.locked = true;
    state.lockedDoc = doc;
    lockScroll(doc);
    }
    if (kind === 'dialog') {
    const nested = !contained && stack.some((other) => MODAL.has(overlays.get(other)?.kind ?? '') && overlays.get(other)?.phase === 'open' && !containedDialog(other));
    element.classList.toggle('ds-dialog--nested', nested);
    }
    }
    stack.push(element);
    enterOrder(element);
    watchRemovals(doc);
    if (!reopening) adopt(element, state);
    if (kind !== 'drawer' && !reopening) element.setAttribute('data-state', transform ? 'open' : 'closed');
    if (kind === 'drawer') {
    app?.setAttribute('data-drawer-open', '');
    element.inert = false;
    if (!state.inerted.length) state.inerted = inertOutside(element);
    } else if (isNative(element) && MODAL.has(kind)) {
    element.classList.remove('ds-dialog--closing', 'ds-sheet--closing');
    if (!element.open) keepFocusVisibility(element, () => element.showModal());
    parts.toast?.host(element, 'enter');
    } else {
    if ( (element).hidden) (element).hidden = false;
    for (const name of ['ds-dialog--hidden', 'ds-sheet--hidden']) {
    if (element.classList.contains(name)) {
    element.classList.remove(name);
    state.hiddenClass = name;
    }
    }
    if (kind === 'dialog' || kind === 'sheet') parts.toast?.host(element, 'enter');
    if (MODAL.has(kind) && !state.inerted.length) state.inerted = inertOutside(element, contained ? containerOf(element) : undefined);
    }
    syncInert();
    box.inert = false;
    if (kind !== 'drawer') {
    if (reopening) {
    element.setAttribute('data-state', 'open');
    if (kind === 'menu' || kind === 'combo') watchEdges(element, state);
    } else if (kind === 'menu' || kind === 'combo') {
    element.setAttribute('data-state', 'closed');
    const style = (element).style;
    style.setProperty('transition', 'none');
    style.setProperty('scale', 'none');
    const anchor = placePopup(element, state);
    const trigger = state.opener && typeof state.opener.getBoundingClientRect === 'function' ? state.opener : anchor;
    if (trigger) originFrom(trigger, element);
    style.removeProperty('scale');
    flushStyle(element);
    style.removeProperty('transition');
    element.setAttribute('data-state', 'open');
    watchEdges(element, state);
    } else if (!transform) {
    element.setAttribute('data-state', 'closed');
    flushStyle(box);
    if ((kind !== 'sheet') && state.opener && typeof state.opener.getBoundingClientRect === 'function') originFrom(state.opener, box);
    element.setAttribute('data-state', 'open');
    }
    }
    keepFocusVisibility(element, () => focusInto(element, state, box));
    state.expander?.setAttribute('aria-expanded', 'true');
    const entrance = settled(box, { transitions: true }).then(() => {
    if (state.gen === gen) state.opening = null;
    });
    state.opening = entrance;
    if (typeof viewOf(element).CustomEvent === 'function') emit(element, 'open', { opener: state.opener ?? null });
    return entrance;
    }
    function close(element, options = {}) {
    if (!element) return Promise.resolve();
    const pending = overlays.get(element);
    if (pending?.container) pending.container = null;
    if (element.matches('.ds-drawer')) watchDrawer(element, drawerQuery(element));
    return closeFrom(element, 'api', typeof options.reason === 'string' && options.reason ? options.reason : 'api');
    }
    function topmost(doc = globalThis.document) {
    if (!doc) return null;
    pruneDetached();
    const order = doc[SHARED]?.order ?? [];
    for (let i = order.length - 1; i >= 0; i -= 1) if (order[i].open()) return order[i].element;
    return null;
    }
    function layerOver(node) {
    const top = topmost(node.ownerDocument);
    if (!top || composedContains(top, node)) return null;
    if (containedDialog(top) && !composedContains(containerOf(top), node)) return null;
    return top;
    }
    function layerAt(node, doc = node?.ownerDocument ?? globalThis.document) {
    if (!doc) return null;
    pruneDetached();
    const order = doc[SHARED]?.order ?? [];
    for (let i = order.length - 1; i >= 0; i -= 1) {
    const { element } = order[i];
    if (!order[i].open()) continue;
    if (containedDialog(element) && !composedContains(containerOf(element), node)) continue;
    return element;
    }
    return null;
    }
    function opensOpenLayer(node) {
    pruneDetached();
    const order = node.ownerDocument?.[SHARED]?.order ?? [];
    return order.some((entry) => entry.open() && entry.opener?.() === node);
    }
    function announceClose(element, reason) {
    emit(element, 'close', { reason });
    }
    function closeFrom(element, cause, reason = cause) {
    const state = overlays.get(element);
    if (!state || state.phase === 'closed') return Promise.resolve();
    if (state.phase === 'closing') return state.closing ?? Promise.resolve();
    if (!element.isConnected) {
    dropDetached();
    return Promise.resolve();
    }
    const kind = state.kind;
    if (MODAL.has(kind) && USER_CLOSE.has(cause) && element.hasAttribute('data-ds-busy')) return Promise.resolve();
    const box = boxOf(element, kind);
    const doc = element.ownerDocument;
    if (MODAL.has(kind)) {
    for (const tip of tooltipControllers) tip.hide();
    for (const other of [...stack].reverse()) {
    const inner = overlays.get(other);
    if (other === element || inner?.phase !== 'open') continue;
    if (element.contains(other) || (!MODAL.has(inner.kind) && element.contains(inner.opener))) closeFrom(other, 'parent');
    }
    }
    const gen = ++state.gen;
    state.phase = 'closing';
    state.opening = null;
    const active = activeIn(element);
    const focusInside = Boolean(active) && (element.contains(active) || active === doc.body);
    if (kind === 'drawer') (state.app ?? element.closest('.ds-app'))?.removeAttribute('data-drawer-open');
    if (isNative(element) && MODAL.has(kind)) {
    element.classList.add(kind === 'sheet' ? 'ds-sheet--closing' : 'ds-dialog--closing');
    if (element.open) keepFocusVisibility(element, () => element.close());
    }
    restoreInert(state);
    state.expander?.setAttribute('aria-expanded', 'false');
    const returns = MODAL.has(kind) ? focusInside : cause !== 'outside' && element.contains(activeIn(element));
    if (returns) keepFocusVisibility(element, () => returnTarget(element, state)?.focus({ preventScroll: true }));
    if (kind === 'dialog' || kind === 'sheet') parts.toast?.host(element, 'exit');
    box.inert = true;
    if (kind !== 'drawer') element.setAttribute('data-state', 'closed');
    const exit = settled(box, { transitions: true }).then(() => {
    if (state.gen !== gen) return;
    state.phase = 'closed';
    state.closing = null;
    if (kind === 'dialog' || kind === 'sheet') parts.toast?.host(element, 'settle');
    if (kind !== 'drawer') {
    if (isNative(element) && MODAL.has(kind)) {
    element.classList.remove('ds-dialog--closing', 'ds-sheet--closing');
    flushStyle(element);
    } else if (state.hiddenClass) element.classList.add(state.hiddenClass);
    else (element).hidden = true;
    if (kind === 'menu' || kind === 'combo') releasePopup(element, state);
    element.removeAttribute('data-state');
    }
    box.inert = false;
    element.classList.remove('ds-dialog--nested');
    giveBack(element, state);
    finishClose(element, state);
    });
    state.closing = exit;
    announceClose(element, reason);
    return exit;
    }
    const normalText = (text) => String(text ?? '').replace(/\s+/g, ' ').trim().toLowerCase();
    const clock = () => (typeof globalThis.performance?.now === 'function' ? globalThis.performance.now() : Date.now());
    function timeScaleOf(element) {
    if (!element?.closest?.('.ds-theme')) return 1;
    const value = parseFloat(viewOf(element).getComputedStyle?.(element).getPropertyValue('--ds-time-scale') ?? '');
    return value > 0 ? value : 1;
    }
    function nextFrame(doc, callback) {
    const view = doc.defaultView ?? globalThis;
    if (doc.hidden || typeof view.requestAnimationFrame !== 'function') setTimeout(callback);
    else view.requestAnimationFrame(callback);
    }
    const savebars = new WeakMap();
    let savebarWarned = false;
    function savebar(element, options = {}) {
    const existing = savebars.get(element);
    if (existing) return existing;
    const doc = element.ownerDocument;
    const view = viewOf(element);
    const bar = (element.matches('.ds-savebar__bar') ? element : element.querySelector('.ds-savebar__bar') ?? element);
    const wrapper = (bar.closest('.ds-savebar'));
    const scroller = ((wrapper ?? bar).closest('.ds-view'));
    if (!scroller && !savebarWarned) {
    savebarWarned = true;
    view.console?.warn('Z-Dash: DS.overlay.savebar() outside .ds-view. The bar shows and hides, but no view keeps room for it or scrolls focus clear of it.');
    }
    let shown = false;
    let gen = 0;
    let height = 0;
    let leaving = (null);
    const pending = ([]);
    let observer = (null);
    let resting = false;
    let restRoom = 0;
    const gap = () => pxOf(bar, '--ds-space-2', 8);
    const offset = () => parseFloat(view.getComputedStyle?.(bar).bottom ?? '') || 0;
    const barTop = () => (wrapper ? wrapper.getBoundingClientRect().bottom - offset() : bar.getBoundingClientRect().bottom) - height;
    const entry = {
    element: bar,
    lift(region) {
    const style = view.getComputedStyle?.(region);
    if (!style) return 0;
    const lifted = parseFloat( (region).style.getPropertyValue('--ds-toast-lift')) || 0;
    const floor = (doc.documentElement.clientHeight || view.innerHeight) - ((parseFloat(style.bottom) || 0) - lifted) - (parseFloat(style.marginBottom) || 0);
    const top = barTop();
    if (top + height <= floor - (region).offsetHeight) return 0;
    return Math.max(0, floor - (top - gap()));
    },
    };
    for (const name of ['hidden', 'inert']) own(bar, 'attrs', name);
    if (scroller) {
    for (const name of ['data-ds-savebar', 'data-ds-savebar-rest']) own(scroller, 'attrs', name);
    own(scroller, 'props', '--ds-savebar-height');
    }
    function regions() {
    const theme = bar.closest('.ds-theme');
    const scope = theme ?? doc;
    return [...scope.querySelectorAll('.ds-toasts')].filter((region) => (!theme || region.closest('.ds-theme') === theme) && !parts.toast?.hosted(region));
    }
    function measure() {
    height = bar.offsetHeight;
    scroller?.style.setProperty('--ds-savebar-height', `${round(height)}px`);
    }
    function flush() {
    while (pending.length) {
    const fn = (pending.shift());
    try {
    fn();
    } catch (error) {
    report(bar, error);
    }
    }
    }
    function onScroll() {
    if (!scroller || scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop >= restRoom) stopRest();
    }
    function startRest() {
    if (!scroller) return;
    restRoom = height + offset() + gap();
    resting = true;
    scroller.setAttribute('data-ds-savebar-rest', '');
    scroller.addEventListener('scroll', onScroll, { passive: true });
    }
    function stopRest() {
    if (!resting || !scroller) return;
    resting = false;
    scroller.removeEventListener('scroll', onScroll);
    scroller.removeAttribute('data-ds-savebar-rest');
    if (!shown) scroller.style.removeProperty('--ds-savebar-height');
    }
    function keepClear() {
    const active = scroller ? activeIn(scroller) : null;
    if (!active || active === scroller || !scroller?.contains(active) || bar.contains(active) || typeof active.scrollIntoView !== 'function') return;
    if (active.getBoundingClientRect().bottom > barTop() - gap()) active.scrollIntoView({ block: 'nearest' });
    }
    function show() {
    if (shown) return;
    shown = true;
    gen += 1;
    leaving = null;
    flush();
    stopRest();
    bar.inert = false;
    bar.hidden = false;
    measure();
    scroller?.setAttribute('data-ds-savebar', '');
    shownSavebars.add(entry);
    for (const region of regions()) parts.toast?.lift(region);
    const Observer = view.ResizeObserver;
    if (!observer && typeof Observer === 'function') {
    observer = new Observer(() => {
    if (!shown || bar.offsetHeight === height) return;
    measure();
    for (const region of regions()) parts.toast?.lift(region);
    });
    observer.observe(bar);
    }
    keepClear();
    }
    function hide() {
    if (!shown) return;
    shown = false;
    const mine = ++gen;
    if (bar.contains(activeIn(bar))) {
    let target = null;
    try {
    target = options.focusTarget?.() ?? null;
    } catch (error) {
    report(bar, error);
    }
    const fallback = scroller ?? doc.querySelector(LANDMARK);
    const next = [target, fallback].find((node) => canFocus(node) && !bar.contains(node));
    next?.focus({ preventScroll: true });
    }
    const atEnd = Boolean(scroller) && scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop <= 1;
    observer?.disconnect();
    observer = null;
    bar.inert = true;
    bar.hidden = true;
    shownSavebars.delete(entry);
    for (const region of regions()) parts.toast?.lift(region);
    if (scroller) {
    scroller.removeAttribute('data-ds-savebar');
    if (atEnd) startRest();
    else scroller.style.removeProperty('--ds-savebar-height');
    }
    leaving = settled(bar, { transitions: true }).then(() => {
    if (mine !== gen) return;
    leaving = null;
    bar.inert = false;
    flush();
    });
    }
    const handle = {
    element,
    show,
    hide,
    update(fn) {
    if (typeof fn !== 'function') return;
    if (leaving) pending.push(fn);
    else fn();
    },
    get shown() {
    return shown;
    },
    destroy() {
    gen += 1;
    observer?.disconnect();
    observer = null;
    leaving = null;
    pending.length = 0;
    stopRest();
    if (shownSavebars.delete(entry)) for (const region of regions()) parts.toast?.lift(region);
    bar.inert = false;
    for (const name of ['hidden', 'inert']) disown(bar, 'attrs', name);
    if (scroller) {
    scroller.removeAttribute('data-ds-savebar');
    scroller.style.removeProperty('--ds-savebar-height');
    for (const name of ['data-ds-savebar', 'data-ds-savebar-rest']) disown(scroller, 'attrs', name);
    disown(scroller, 'props', '--ds-savebar-height');
    }
    savebars.delete(element);
    },
    };
    savebars.set(element, handle);
    if (!bar.hidden) {
    bar.hidden = true;
    show();
    }
    return handle;
    }
    function confirm(options = {}) {
    return partFor('dialog', 'confirm').confirm(options);
    }
    function alert(options = {}) {
    return partFor('dialog', 'alert').alert(options);
    }
    function prompt(options = {}) {
    return partFor('dialog', 'prompt').prompt(options);
    }
    function toastRegion(doc = globalThis.document) {
    return partFor('toast', 'toastRegion').toastRegion(doc);
    }
    function announce(text, options = {}) {
    return partFor('toast', 'announce').announce(text, options);
    }
    function toast(region, content, options = {}) {
    return partFor('toast', 'toast').toast(region, content, options);
    }
    function closeToasts(region, filter = {}) {
    return partFor('toast', 'closeToasts').closeToasts(region, filter);
    }
    function tooltips(root = globalThis.document) {
    return partFor('tooltip', 'tooltips').tooltips(root);
    }
    function targetOf(trigger, value) {
    const scope = trigger.getRootNode?.() ?? trigger.ownerDocument;
    let found = null;
    try {
    found = scope.querySelector(value);
    } catch {
    }
    return found ?? scope.getElementById?.(value) ?? null;
    }
    function isOpen(element) {
    return overlays.get(element)?.phase === 'open';
    }
    define('triggers', {
    global(root) {
    const onClick = ( event) => {
    if (event.defaultPrevented || event.detail > 1) return;
    const trigger = (event.target)?.closest?.('[data-ds-open], [data-ds-close]');
    if (!trigger) return;
    const opens = trigger.getAttribute('data-ds-open');
    if (opens) {
    const target = targetOf(trigger, opens);
    if (!target) return;
    if (isOpen(target) && overlays.get(target)?.opener === trigger) closeFrom(target, 'close');
    else open(target, { opener: trigger });
    return;
    }
    const closes = trigger.getAttribute('data-ds-close');
    const target = closes ? targetOf(trigger, closes) : null;
    if (target && isOpen(target)) closeFrom(target, 'close');
    };
    const onKeyDown = ( event) => {
    if (event.defaultPrevented || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return;
    const trigger = (event.target)?.closest?.('[data-ds-open]');
    const target = trigger ? targetOf(trigger, trigger.getAttribute('data-ds-open') ?? '') : null;
    if (!target?.matches('.ds-menu') || isOpen(target)) return;
    event.preventDefault();
    open(target, { opener: trigger });
    };
    root.addEventListener('click', onClick);
    root.addEventListener('keydown', onKeyDown);
    return () => {
    root.removeEventListener('click', onClick);
    root.removeEventListener('keydown', onKeyDown);
    };
    },
    });
    function controllerOf(element) {
    const listeners = ([]);
    return {
    element,
    get: () => isOpen(element),
    set: (value, options) => (value ? open(element, options) : close(element, options)),
    on(type, fn) {
    const name = `ds:${type}`;
    element.addEventListener(name, fn);
    const off = () => element.removeEventListener(name, fn);
    listeners.push(off);
    return off;
    },
    destroy() {
    for (const off of listeners.splice(0)) off();
    },
    };
    }
    const internals = Object.freeze({
    overlays,
    stack,
    tooltipControllers,
    neverInert,
    shownSavebars,
    LANDMARK,
    activeIn,
    inside,
    sharedOf,
    own,
    disown,
    viewOf,
    flushStyle,
    canFocus,
    report,
    watchModality,
    composedContains,
    keepAway,
    keepHome,
    closeFrom,
    layerOver,
    opensOpenLayer,
    arrowTarget,
    topOpen,
    nextAnchorName,
    placeMenu,
    isOpen,
    controllerOf,
    clock,
    normalText,
    timeScaleOf,
    nextFrame,
    part: (name) => parts[name] ?? null,
    use(name, part) {
    parts[name] ??= part;
    pageParts[name] ??= part;
    },
    });

    return Object.freeze({
      TOAST_MS: TOAST_MS,
      TOAST_MAX_MS: TOAST_MAX_MS,
      TOAST_LIMIT: TOAST_LIMIT,
      TOOLTIP_DELAY_MS: TOOLTIP_DELAY_MS,
      TOOLTIP_SKIP_MS: TOOLTIP_SKIP_MS,
      TOOLTIP_HIDE_MS: TOOLTIP_HIDE_MS,
      DISMISS_DELAY_MS: DISMISS_DELAY_MS,
      DRAWER_QUERY: DRAWER_QUERY,
      FOCUSABLE: FOCUSABLE,
      canFocus: canFocus,
      holdInert: holdInert,
      lockScroll: lockScroll,
      unlockScroll: unlockScroll,
      typeAhead: typeAhead,
      open: open,
      close: close,
      topmost: topmost,
      layerAt: layerAt,
      savebar: savebar,
      confirm: confirm,
      alert: alert,
      prompt: prompt,
      toastRegion: toastRegion,
      announce: announce,
      toast: toast,
      closeToasts: closeToasts,
      tooltips: tooltips,
      internals: internals
    });
  });

  add('a11y', ['canTakeFocus', 'tabbables', 'readsKeys', 'trap', 'roving', 'typeaheadMatch', 'typeahead', 'announce', 'inert'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const runtime = DS.core.runtime;
    const motionDuration = DS.motion.motionDuration;
    const rovingOf = DS.dom.roving, typesText = DS.dom.typesText;
    const announceOf = DS.overlay.announce, canFocus = DS.overlay.canFocus, FOCUSABLE = DS.overlay.FOCUSABLE, holdInert = DS.overlay.holdInert, layerAt = DS.overlay.layerAt, typeAhead = DS.overlay.typeAhead;
    const shared = runtime.state('a11y', () => ({ stacks: new WeakMap(), traps: new WeakMap() }));
    const isRadio = (node) => node.tagName === 'INPUT' && node.type === 'radio' && Boolean(node.name);
    function canTakeFocus(node) {
    if (!canFocus(node) || node.matches?.(':disabled')) return false;
    return typeof node.checkVisibility !== 'function' || node.checkVisibility({ checkVisibilityCSS: true });
    }
    function tabbables(root) {
    if (!root?.querySelectorAll) return [];
    const candidates = [...root.querySelectorAll(FOCUSABLE)].filter((node) => Number(node.getAttribute('tabindex')) >= 0 && canTakeFocus(node));
    const groups = new Map();
    for (const node of candidates) {
    if (!isRadio(node)) continue;
    if (!groups.has(node.name) || node.checked) groups.set(node.name, node);
    }
    return candidates.filter((node) => !isRadio(node) || groups.get(node.name) === node);
    }
    const KEY_WIDGETS = '[role="textbox"], [role="searchbox"], [role="combobox"], [role="spinbutton"], [role="listbox"], [role="menu"], [role="menubar"], [role="tree"], [role="treegrid"]';
    function readsKeys(node) {
    if (!node?.closest) return false;
    return typesText(node) || node.tagName === 'SELECT' || Boolean(node.closest(KEY_WIDGETS));
    }
    function deepActive(doc) {
    let active = doc.activeElement;
    while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement;
    return active;
    }
    function aroundNode(list, node, backwards) {
    if (backwards) return [...list].reverse().find((each) => node.compareDocumentPosition(each) & node.DOCUMENT_POSITION_PRECEDING) ?? list[list.length - 1];
    return list.find((each) => node.compareDocumentPosition(each) & node.DOCUMENT_POSITION_FOLLOWING) ?? list[0];
    }
    const holds = (item) => canTakeFocus(item.container) || tabbables(item.container).length > 0;
    function pruneRemoved(stack) {
    for (const item of [...stack]) if (!item.container.isConnected) item.destroy();
    }
    function trap(container, options = {}) {
    const doc = container?.ownerDocument;
    if (!doc) return { destroy() {} };
    const existing = shared.traps.get(container);
    if (existing) return existing;
    const scope = container.getRootNode?.() ?? doc;
    const activeNow = () => scope.activeElement ?? null;
    const before = deepActive(doc);
    const addedTabindex = !container.hasAttribute('tabindex');
    const stack = shared.stacks.get(doc) ?? [];
    shared.stacks.set(doc, stack);
    const entry = { last: null, container, destroy: () => handle.destroy() };
    stack.push(entry);
    const focusBox = () => {
    if (!container.hasAttribute('tabindex')) container.setAttribute('tabindex', '-1');
    (container).focus();
    };
    const under = (node) => {
    const layer = node ? layerAt(node, doc) : null;
    return Boolean(layer && !layer.contains(container));
    };
    const resting = (node) => [...stack].reverse().find(holds) !== entry || under(container) || under(node);
    const onKey = ( event) => {
    if (event.key !== 'Tab') return;
    pruneRemoved(stack);
    if (event.defaultPrevented || resting(deepActive(doc))) return;
    const active = activeNow();
    const toasts = active?.closest?.('.ds-toasts');
    if (toasts) {
    const own = tabbables(toasts);
    if (active !== own[event.shiftKey ? 0 : own.length - 1]) return;
    }
    const list = tabbables(container);
    const first = list[0];
    const last = list[list.length - 1];
    let next = null;
    if (!first) next = container;
    else if (!active || !container.contains(active)) next = event.shiftKey ? last : first;
    else if (!list.includes(active)) next = aroundNode(list, active, event.shiftKey);
    else if (event.shiftKey && active === first) next = last;
    else if (!event.shiftKey && active === last) next = first;
    if (!next) return;
    event.preventDefault();
    if (next === container) focusBox();
    else next.focus();
    };
    const onFocus = ( event) => {
    pruneRemoved(stack);
    const path = event.composedPath?.() ?? [event.target];
    if (path.includes(container)) {
    entry.last = path[0];
    return;
    }
    if (resting(path[0]) || path.some((node) => node.matches?.('.ds-toasts'))) return;
    const back = canTakeFocus(entry.last) && container.contains(entry.last) ? entry.last : tabbables(container)[0];
    if (back) back.focus();
    else focusBox();
    };
    doc.addEventListener('keydown', onKey, true);
    doc.addEventListener('focusin', onFocus, true);
    const active = activeNow();
    if (options.initial !== false && (options.initial !== undefined || !active || !container.contains(active))) {
    const wanted = typeof options.initial === 'string' ? container.querySelector(options.initial)
    : options.initial ?? container.querySelector('[autofocus]');
    const target = canTakeFocus(wanted) ? wanted : tabbables(container)[0];
    if (target) (target).focus();
    else focusBox();
    } else if (active && container.contains(active)) entry.last = active;
    const handle = {
    destroy() {
    if (shared.traps.get(container) !== handle) return;
    shared.traps.delete(container);
    stack.splice(stack.indexOf(entry), 1);
    doc.removeEventListener('keydown', onKey, true);
    doc.removeEventListener('focusin', onFocus, true);
    const now = doc.activeElement;
    const lost = !now || now === doc.body || container.contains(activeNow());
    if (addedTabindex) container.removeAttribute('tabindex');
    const home = options.returnFocus === undefined ? before : options.returnFocus;
    if (home && lost && canTakeFocus(home)) (home).focus();
    },
    };
    shared.traps.set(container, handle);
    return handle;
    }
    function roving(container, options = {}) {
    return rovingOf(container, options);
    }
    const TYPEAHEAD_ITEMS = '[role="option"], [role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"], [role="treeitem"], [role="tab"], [role="radio"]';
    const labelOf = (item) => (item.getAttribute?.('aria-label') || item.textContent || '').replace(/\s+/g, ' ').trim().toLowerCase();
    function typeaheadMatch(items, current, typed, label = labelOf) {
    return typeAhead(items, current, typed, label);
    }
    const typeaheads = new WeakMap();
    function typeahead(container, options = {}) {
    if (!container?.addEventListener) return { destroy() {} };
    const existing = typeaheads.get(container);
    if (existing) return existing;
    const selector = options.items ?? TYPEAHEAD_ITEMS;
    const items = () => (typeof selector === 'function' ? [...selector()] : [...container.querySelectorAll(selector)]).filter(canTakeFocus);
    const currentOf = (list, target) => {
    const holding = list.find((item) => item === target || item.contains(target));
    if (holding) return holding;
    const named = container.getAttribute?.('aria-activedescendant');
    return (named ? container.getRootNode().getElementById?.(named) : null) ?? options.current?.();
    };
    let typed = '';
    let typedAt = 0;
    const onKey = ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    const target = (event.target);
    if (typesText(target) || target?.tagName === 'SELECT') return;
    const key = event.key ?? '';
    if (key.length !== 1) {
    if (/^(arrow|home|end|page)/i.test(key)) typed = '';
    return;
    }
    if (event.timeStamp - typedAt > motionDuration(container, '--ds-delay-tooltip')) typed = '';
    if (key === ' ' && !typed) return;
    typedAt = event.timeStamp;
    typed += key.toLowerCase();
    const list = items();
    const match = typeaheadMatch(list, currentOf(list, target), typed, options.label);
    if (!match) return;
    event.preventDefault();
    if (typeof options.onMatch === 'function') options.onMatch(match);
    else (match).focus();
    };
    container.addEventListener('keydown', onKey);
    const handle = {
    destroy() {
    if (typeaheads.get(container) !== handle) return;
    typeaheads.delete(container);
    container.removeEventListener('keydown', onKey);
    },
    };
    typeaheads.set(container, handle);
    return handle;
    }
    function announce(text, options = {}) {
    announceOf(text, options);
    }
    function inert(elements) {
    if (!elements) return holdInert([]);
    return holdInert(elements.nodeType ? [elements] : elements);
    }

    return Object.freeze({
      canTakeFocus: canTakeFocus,
      tabbables: tabbables,
      readsKeys: readsKeys,
      trap: trap,
      roving: roving,
      typeaheadMatch: typeaheadMatch,
      typeahead: typeahead,
      announce: announce,
      inert: inert
    });
  });

  add('format', ['number', 'percent', 'currency', 'bytes', 'duration', 'list', 'plural', 'relative', 'absolute', 'clock', 'time'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const config = DS.core.config, define = DS.core.define, runtime = DS.core.runtime;
    const SECOND = 1000;
    const MINUTE = 60 * SECOND;
    const HOUR = 60 * MINUTE;
    const DAY = 24 * HOUR;
    const WEEK = 7 * DAY;
    const MONTH = 30.436875 * DAY;
    const YEAR = 365.2425 * DAY;
    const RELATIVE_UNITS = Object.freeze([['year', YEAR], ['month', MONTH], ['week', WEEK], ['day', DAY], ['hour', HOUR], ['minute', MINUTE], ['second', SECOND]]);
    const DURATION_UNITS = Object.freeze([['day', DAY], ['hour', HOUR], ['minute', MINUTE], ['second', SECOND], ['millisecond', 1]]);
    const BYTE_UNITS = Object.freeze(['byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte', 'petabyte']);
    const BINARY_SYMBOLS = Object.freeze(['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']);
    const FORMATTERS = 256;
    const SHORTEST_WAIT = 200;
    const LONGEST_WAIT = 2 ** 31 - 1;
    const TIME_OPTIONS = Object.freeze(['style', 'numeric', 'smallest', 'largest']);
    const shared = runtime.state('format', () => ({
    clients: new Set(),
    timer: null,
    listening: null,
    times: new WeakMap(),
    }));
    const formatters = new Map();
    const accepted = new Map();
    function localeFor(options) {
    const locale = options?.locale ?? config().locale;
    if (!accepted.has(locale)) {
    let good = locale;
    try {
    Intl.getCanonicalLocales(locale);
    } catch {
    good = undefined;
    }
    accepted.set(locale, good);
    }
    return accepted.get(locale);
    }
    function formatter(Type, name, locale, settings) {
    const key = `${name}|${locale ?? ''}|${JSON.stringify(settings)}`;
    let made = formatters.get(key);
    if (!made) {
    made = new Type(locale, settings);
    if (formatters.size >= FORMATTERS) formatters.clear();
    formatters.set(key, made);
    }
    return made;
    }
    function intlOptions(options, ...own) {
    const settings = { ...options };
    for (const name of ['locale', 'empty', ...own]) delete settings[name];
    return settings;
    }
    const numberFormat = (locale, settings) => formatter(Intl.NumberFormat, 'number', locale, settings);
    function numeric(value) {
    if (typeof value === 'bigint') return value;
    if (typeof value !== 'number' && (typeof value !== 'string' || !value.trim())) return null;
    const parsed = Number(value);
    return Number.isNaN(parsed) ? null : parsed;
    }
    function toDate(value) {
    if (value === null || value === undefined || value === '') return null;
    const date = value instanceof Date ? value : new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
    }
    function number(value, options = {}) {
    const parsed = numeric(value);
    if (parsed === null) return options.empty ?? '';
    return numberFormat(localeFor(options), intlOptions(options)).format(parsed);
    }
    function percent(value, options = {}) {
    return number(value, { ...options, style: 'percent' });
    }
    function currency(value, code, options = {}) {
    return number(value, { ...options, style: 'currency', currency: code });
    }
    function bytes(value, options = {}) {
    const parsed = numeric(value);
    if (parsed === null) return options.empty ?? '';
    const base = options.base === 1024 ? 1024 : 1000;
    const size = Number(parsed);
    const magnitude = Math.abs(size);
    let power = magnitude >= 1 ? Math.min(Math.floor(Math.log(magnitude) / Math.log(base)), BYTE_UNITS.length - 1) : 0;
    const digits = options.maximumFractionDigits ?? 1;
    const places = 10 ** digits;
    if (power < BYTE_UNITS.length - 1 && Math.round((magnitude / base ** power) * places) / places >= base) power += 1;
    const settings = intlOptions(options, 'base');
    if (settings.maximumFractionDigits === undefined) settings.maximumFractionDigits = Math.max(power === 0 ? 0 : 1, settings.minimumFractionDigits ?? 0);
    const figure = size / base ** power;
    const locale = localeFor(options);
    if (base === 1024 || (power === 0 && !settings.unitDisplay)) return `${numberFormat(locale, settings).format(figure)} ${BINARY_SYMBOLS[power]}`;
    return numberFormat(locale, { ...settings, style: 'unit', unit: BYTE_UNITS[power] }).format(figure);
    }
    function duration(value, options = {}) {
    const parsed = numeric(value);
    if (parsed === null) return options.empty ?? '';
    const ms = Number(parsed);
    if (!Number.isFinite(ms)) return options.empty ?? '';
    const negative = ms < 0;
    const locale = localeFor(options);
    const style = options.style ?? 'short';
    if (style === 'clock') {
    const total = Math.round(Math.abs(ms) / SECOND);
    const hours = Math.floor(total / 3600);
    const pad = numberFormat(locale, { minimumIntegerDigits: 2, useGrouping: false });
    const plain = numberFormat(locale, { useGrouping: false });
    const minutes = Math.floor(total / 60) % 60;
    const seconds = pad.format(total % 60);
    const lead = hours || minutes;
    const first = plain.format(negative ? -lead : lead);
    return hours ? `${first}:${pad.format(minutes)}:${seconds}` : `${first}:${seconds}`;
    }
    const magnitude = Math.abs(ms);
    const named = DURATION_UNITS.findIndex(([unit]) => unit === options.smallest);
    const floor = named >= 0 ? named : magnitude > 0 && magnitude < SECOND - 0.5 ? DURATION_UNITS.length - 1 : DURATION_UNITS.length - 2;
    const parts = Math.max(1, options.parts ?? 2);
    const fit = (amount) => {
    const largest = DURATION_UNITS.findIndex(([, size]) => amount >= size);
    const last = Math.min(floor, (largest < 0 ? floor : largest) + parts - 1);
    const size = DURATION_UNITS[last][1];
    return Math.round(amount / size) * size;
    };
    let left = fit(fit(magnitude));
    const pieces = [];
    for (const [index, [unit, size]] of DURATION_UNITS.entries()) {
    if (index > floor) break;
    const count = Math.floor(left / size + 1e-9);
    if (count) {
    pieces.push([unit, count]);
    left -= count * size;
    }
    }
    if (!pieces.length) pieces.push([DURATION_UNITS[floor][0], 0]);
    const formatted = pieces.map(([unit, count], index) => numberFormat(locale, { style: 'unit', unit, unitDisplay: style }).format(negative && index === 0 ? -count : count));
    return formatter(Intl.ListFormat, 'list', locale, { type: 'unit', style }).format(formatted);
    }
    function list(items, options = {}) {
    const texts = Array.from(items ?? [], (item) => (item === null || item === undefined ? '' : String(item))).filter(Boolean);
    if (!texts.length) return options.empty ?? '';
    return formatter(Intl.ListFormat, 'list', localeFor(options), intlOptions(options)).format(texts);
    }
    function plural(count, forms, options = {}) {
    const parsed = numeric(count);
    if (parsed === null) return options.empty ?? '';
    const amount = Number(parsed);
    const locale = localeFor(options);
    const category = formatter(Intl.PluralRules, 'plural', locale, { type: options.type ?? 'cardinal' }).select(amount);
    const form = forms?.[`=${amount}`] ?? forms?.[category] ?? forms?.other;
    if (form === undefined || form === null) return options.empty ?? '';
    const formatted = numberFormat(locale, intlOptions(options, 'type')).format(amount);
    return typeof form === 'function' ? String(form({ count: amount, formatted })) : String(form).replace(/\{count\}/g, formatted);
    }
    function relativeParts(difference, options) {
    const magnitude = Math.abs(difference);
    const named = (name, otherwise) => {
    const index = RELATIVE_UNITS.findIndex(([unit]) => unit === name);
    return index >= 0 ? index : otherwise;
    };
    const first = named(options.largest, 0);
    const last = Math.max(first, named(options.smallest, RELATIVE_UNITS.length - 1));
    let at = first;
    while (at < last && magnitude < RELATIVE_UNITS[at][1]) at += 1;
    const [unit, size] = RELATIVE_UNITS[at];
    return { unit, size, amount: Math.floor(magnitude / size) };
    }
    function relative(value, options = {}) {
    const date = toDate(value);
    if (!date) return options.empty ?? '';
    const now = options.now === undefined ? Date.now() : Number(options.now);
    const difference = date.getTime() - now;
    const { unit, amount } = relativeParts(difference, options);
    const settings = { style: options.style ?? 'long', numeric: options.numeric ?? 'auto' };
    return formatter(Intl.RelativeTimeFormat, 'relative', localeFor(options), settings).format(difference < 0 ? -amount : amount, unit);
    }
    function absolute(value, options = {}) {
    const date = toDate(value);
    if (!date) return options.empty ?? '';
    const settings = intlOptions(options);
    const named = Object.keys(settings).some((key) => key !== 'timeZone' && key !== 'hour12' && key !== 'hourCycle' && key !== 'calendar' && key !== 'numberingSystem');
    return formatter(Intl.DateTimeFormat, 'date', localeFor(options), named ? settings : { dateStyle: 'medium', timeStyle: 'short', ...settings }).format(date);
    }
    const pageHidden = () => globalThis.document?.visibilityState === 'hidden';
    function runClock(all = false) {
    shared.timer = null;
    const now = Date.now();
    for (const client of [...shared.clients]) {
    if (!all && client.due > now) continue;
    runClient(client, now);
    }
    armClock();
    }
    function runClient(client, now) {
    let wait;
    try {
    wait = client.fn(now);
    } catch (error) {
    if (typeof globalThis.reportError === 'function') globalThis.reportError(error);
    else globalThis.console?.error?.(error);
    }
    client.due = now + (typeof wait === 'number' && wait >= 0 ? wait : SECOND);
    }
    function armClock() {
    clearTimeout(shared.timer);
    shared.timer = null;
    if (!shared.clients.size) {
    shared.listening?.();
    shared.listening = null;
    return;
    }
    const doc = globalThis.document;
    if (doc && !shared.listening && typeof doc.addEventListener === 'function') {
    const wake = () => {
    if (pageHidden()) {
    clearTimeout(shared.timer);
    shared.timer = null;
    } else runClock(true);
    };
    const reformat = ( event) => {
    if (event.detail?.changed?.includes('locale') && !pageHidden()) runClock(true);
    };
    doc.addEventListener('visibilitychange', wake);
    doc.addEventListener('ds:config', reformat);
    shared.listening = () => {
    doc.removeEventListener('visibilitychange', wake);
    doc.removeEventListener('ds:config', reformat);
    };
    }
    if (pageHidden()) return;
    let soonest = Infinity;
    for (const client of shared.clients) soonest = Math.min(soonest, client.due);
    shared.timer = setTimeout(() => runClock(), Math.min(LONGEST_WAIT, Math.max(SHORTEST_WAIT, soonest - Date.now())));
    shared.timer?.unref?.();
    }
    function clock(fn) {
    const client = { fn, due: 0 };
    shared.clients.add(client);
    runClient(client, Date.now());
    armClock();
    return () => {
    if (shared.clients.delete(client)) armClock();
    };
    }
    const OWNED = Symbol.for('ds.owned');
    function own(element, name) {
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record.attrs.set(name, (record.attrs.get(name) ?? 0) + 1);
    }
    function disown(element, name) {
    const map = element[OWNED]?.attrs;
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    function nextChange(difference, size) {
    const magnitude = Math.abs(difference);
    const rest = magnitude % size;
    return Math.min(HOUR, difference < 0 ? size - rest : rest + 1);
    }
    function time(el, options = {}) {
    const idle = { get: () => ({ value: null, text: '' }), set() {}, on: () => () => {}, update() {}, destroy() {} };
    if (!el?.ownerDocument) return idle;
    const existing = shared.times.get(el);
    if (existing) return existing;
    const listeners = new Set();
    let live = true;
    let seen = el.isConnected;
    let wroteTip = false;
    let stop = () => {};
    let watcher = null;
    const source = () => el.getAttribute('data-ds-time') || el.getAttribute('datetime') || null;
    const setting = (name) => options[name] ?? (el.getAttribute(`data-ds-time-${name}`) || undefined);
    const settings = () => {
    const out = {};
    for (const name of TIME_OPTIONS) {
    const value = setting(name);
    if (value !== undefined) out[name] = value;
    }
    if (options.locale) out.locale = options.locale;
    return out;
    };
    const writeTip = (date) => {
    const off = options.tip === false || el.getAttribute('data-ds-time-tip') === 'off';
    if (off || !date) {
    if (wroteTip) {
    wroteTip = false;
    el.removeAttribute('data-tip');
    disown(el, 'data-tip');
    }
    return;
    }
    if (!wroteTip) {
    if (el.hasAttribute('data-tip')) return;
    wroteTip = true;
    own(el, 'data-tip');
    }
    const text = absolute(date, options.locale ? { locale: options.locale } : {});
    if (el.getAttribute('data-tip') !== text) el.setAttribute('data-tip', text);
    };
    const render = (now = Date.now()) => {
    const date = toDate(source());
    writeTip(date);
    if (!date) return HOUR;
    const current = settings();
    const text = relative(date, { ...current, now });
    if (el.textContent !== text) {
    if (el.childNodes.length === 1 && el.firstChild.nodeType === 3) el.firstChild.nodeValue = text;
    else el.textContent = text;
    watcher?.takeRecords();
    for (const fn of [...listeners]) fn({ value: date, text });
    }
    return nextChange(date.getTime() - now, relativeParts(date.getTime() - now, current).size);
    };
    const controller = {
    get() {
    return { value: toDate(source()), text: el.textContent ?? '' };
    },
    set(value) {
    const date = toDate(value);
    const name = el.getAttribute('data-ds-time') || el.localName !== 'time' ? 'data-ds-time' : 'datetime';
    el.setAttribute(name, date ? date.toISOString() : '');
    render();
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    update() {
    if (live) render();
    },
    destroy() {
    if (!live) return;
    live = false;
    stop();
    watcher?.disconnect();
    listeners.clear();
    if (wroteTip) {
    wroteTip = false;
    el.removeAttribute('data-tip');
    disown(el, 'data-tip');
    }
    if (shared.times.get(el) === controller) shared.times.delete(el);
    },
    };
    shared.times.set(el, controller);
    if (options.value !== undefined) {
    const date = toDate(options.value);
    if (date) el.setAttribute(el.getAttribute('data-ds-time') || el.localName !== 'time' ? 'data-ds-time' : 'datetime', date.toISOString());
    }
    const Observer = el.ownerDocument.defaultView?.MutationObserver ?? globalThis.MutationObserver;
    if (typeof Observer === 'function') {
    watcher = new Observer(() => {
    if (live) render();
    });
    watcher.observe(el, { childList: true, characterData: true, subtree: true });
    }
    if (!seen) render();
    stop = clock((now) => {
    if (!el.isConnected) {
    if (seen) {
    controller.destroy();
    return HOUR;
    }
    return SECOND;
    }
    seen = true;
    return render(now);
    });
    return controller;
    }
    define('time', {
    attribute: 'data-ds-time',
    attributes: ['datetime', ...TIME_OPTIONS.map((name) => `data-ds-time-${name}`), 'data-ds-time-tip'],
    init: (el) => time(el),
    });

    return Object.freeze({
      number: number,
      percent: percent,
      currency: currency,
      bytes: bytes,
      duration: duration,
      list: list,
      plural: plural,
      relative: relative,
      absolute: absolute,
      clock: clock,
      time: time
    });
  });

  add('store', ['available', 'open'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const emit = DS.core.emit, runtime = DS.core.runtime;
    const PREFIX = 'ds';
    const shared = runtime.state('store', () => ({
    memory: new Map(),
    listeners: new Set(),
    handles: new Set(),
    stop: null,
    }));
    const memoryOf = (area) => shared.memory.get(area);
    function remember(area, full, raw) {
    if (!shared.memory.has(area)) shared.memory.set(area, new Map());
    shared.memory.get(area).set(full, raw);
    }
    function forget(area, full) {
    const values = shared.memory.get(area);
    if (values?.delete(full) && !values.size) shared.memory.delete(area);
    }
    function defaultStorage() {
    try {
    return globalThis.localStorage ?? null;
    } catch {
    return null;
    }
    }
    function attempt(work) {
    try {
    return work();
    } catch {
    return undefined;
    }
    }
    function publish(area, namespace, detail) {
    const full = { namespace, ...detail };
    for (const listener of [...shared.listeners]) {
    if (listener.namespace !== namespace || listener.area !== area) continue;
    try {
    listener.fn(full);
    } catch (error) {
    if (typeof globalThis.reportError === 'function') globalThis.reportError(error);
    else globalThis.console?.error?.(error);
    }
    }
    const doc = globalThis.document;
    if (typeof doc?.dispatchEvent === 'function') emit(doc, 'store', full);
    }
    const parse = (raw) => {
    if (raw === null || raw === undefined) return undefined;
    try {
    return JSON.parse(raw);
    } catch {
    return undefined;
    }
    };
    function onStorage(event) {
    if (event.key === null) {
    for (const key of [...(memoryOf(event.storageArea)?.keys() ?? [])]) if (key.startsWith(`${PREFIX}:`)) forget(event.storageArea, key);
    const told = new Set();
    for (const { namespace, area } of [...shared.handles, ...shared.listeners]) {
    if (area !== event.storageArea || told.has(namespace)) continue;
    told.add(namespace);
    publish(area, namespace, { key: null, value: undefined, previous: undefined, external: true });
    }
    return;
    }
    if (!event.key.startsWith(`${PREFIX}:`)) return;
    const separator = event.key.indexOf(':', PREFIX.length + 1);
    if (separator < 0) return;
    forget(event.storageArea, event.key);
    publish(event.storageArea, event.key.slice(PREFIX.length + 1, separator), {
    key: event.key.slice(separator + 1),
    value: parse(event.newValue),
    previous: parse(event.oldValue),
    external: true,
    });
    }
    function listenStorage() {
    if (shared.stop) return;
    const view = globalThis.window ?? globalThis;
    if (typeof view.addEventListener !== 'function') return;
    view.addEventListener('storage', onStorage);
    shared.stop = () => view.removeEventListener('storage', onStorage);
    }
    function unlistenStorage() {
    if (shared.handles.size || shared.listeners.size) return;
    shared.stop?.();
    shared.stop = null;
    }
    function available(options = {}) {
    const storage = options.storage === undefined ? defaultStorage() : options.storage;
    if (!storage) return false;
    const probe = `${PREFIX}:__probe__`;
    try {
    storage.setItem(probe, '1');
    storage.removeItem(probe);
    return true;
    } catch {
    return false;
    }
    }
    function open(namespace, options = {}) {
    if (typeof namespace !== 'string' || !namespace || namespace.includes(':')) throw new TypeError(`DS.store.open: "${namespace}" is no namespace (a text without a colon)`);
    const storage = options.storage === undefined ? defaultStorage() : options.storage;
    const prefix = `${PREFIX}:${namespace}:`;
    const mine = new Set();
    const record = { namespace, area: storage };
    shared.handles.add(record);
    listenStorage();
    const fullKey = (key) => `${prefix}${key}`;
    const readRaw = (full) => {
    const kept = memoryOf(storage);
    if (kept?.has(full)) return kept.get(full);
    return storage ? attempt(() => storage.getItem(full)) ?? null : null;
    };
    const handle = {
    get(key, fallback) {
    const value = parse(readRaw(fullKey(key)));
    return value === undefined ? fallback : value;
    },
    set(key, value) {
    const full = fullKey(key);
    const raw = JSON.stringify(value);
    if (raw === undefined) {
    handle.remove(key);
    return;
    }
    const before = readRaw(full);
    if (raw === before) return;
    let written = false;
    if (storage) {
    try {
    storage.setItem(full, raw);
    written = true;
    } catch {
    }
    }
    if (written) forget(storage, full);
    else remember(storage, full, raw);
    publish(storage, namespace, { key, value: parse(raw), previous: parse(before), external: false });
    },
    remove(key) {
    const full = fullKey(key);
    const before = readRaw(full);
    forget(storage, full);
    if (storage) attempt(() => storage.removeItem(full));
    if (before !== null) publish(storage, namespace, { key, value: undefined, previous: parse(before), external: false });
    },
    clear() {
    for (const key of handle.keys()) handle.remove(key);
    },
    keys() {
    const found = new Set();
    if (storage) {
    const length = attempt(() => storage.length) ?? 0;
    for (let index = 0; index < length; index += 1) {
    const name = attempt(() => storage.key(index));
    if (name?.startsWith(prefix)) found.add(name.slice(prefix.length));
    }
    }
    for (const name of memoryOf(storage)?.keys() ?? []) if (name.startsWith(prefix)) found.add(name.slice(prefix.length));
    return [...found].sort();
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    const listener = { namespace, area: storage, fn };
    shared.listeners.add(listener);
    mine.add(listener);
    listenStorage();
    return () => {
    shared.listeners.delete(listener);
    mine.delete(listener);
    unlistenStorage();
    };
    },
    destroy() {
    for (const listener of mine) shared.listeners.delete(listener);
    mine.clear();
    shared.handles.delete(record);
    unlistenStorage();
    },
    };
    return handle;
    }

    return Object.freeze({
      available: available,
      open: open
    });
  });

  add('virtual', ['sizes', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const emit = DS.core.emit;
    const morphHtml = DS.dom.morphHtml;
    const ESTIMATE = 44;
    const OVERSCAN = 4;
    const BLIND_ROWS = 8;
    const MAX_PASSES = 4;
    const TAKE_OVER = ['wheel', 'touchstart', 'pointerdown'];
    const EPSILON = 0.01;
    const TEXT_ENTRY = 'input, textarea, select, [contenteditable]:not([contenteditable="false"])';
    const ROW_STOP = 'a[href], area[href], button, input:not([type="hidden"]), select, textarea, summary, iframe, audio[controls], video[controls], [contenteditable]:not([contenteditable="false"]), [tabindex]';
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const setAttribute = (element, name, value) => {
    if (element.getAttribute(name) !== value) element.setAttribute(name, value);
    };
    const clamp = (value, low, high) => Math.min(Math.max(value, low), high);
    function sizes(count = 0, estimate = ESTIMATE) {
    let guess = estimate;
    let length = 0;
    let values = new Float64Array(0);
    let tree = new Float64Array(1);
    let known = new Uint8Array(0);
    let sum = 0;
    let top = 1;
    const guessAt = (index) => {
    const value = typeof guess === 'function' ? Number(guess(index)) : Number(guess);
    return value > 0 ? value : ESTIMATE;
    };
    const rebuild = () => {
    tree = new Float64Array(length + 1);
    sum = 0;
    for (let index = 0; index < length; index += 1) {
    const at = index + 1;
    tree[at] += values[index];
    sum += values[index];
    const parent = at + (at & -at);
    if (parent <= length) tree[parent] += tree[at];
    }
    top = 1;
    while (top * 2 <= length) top *= 2;
    };
    const model = {
    get count() {
    return length;
    },
    size: (index) => values[index] ?? 0,
    measured: (index) => known[index] === 1,
    set(index, height) {
    if (index < 0 || index >= length) return 0;
    const delta = height - values[index];
    known[index] = 1;
    if (!delta) return 0;
    values[index] = height;
    sum += delta;
    for (let at = index + 1; at <= length; at += at & -at) tree[at] += delta;
    return delta;
    },
    offset(index) {
    let total = 0;
    for (let at = clamp(index, 0, length); at > 0; at -= at & -at) total += tree[at];
    return total;
    },
    total: () => sum,
    indexAt(offset) {
    if (!length) return 0;
    if (offset <= 0) return 0;
    let at = 0;
    let left = offset;
    for (let step = top; step > 0; step = Math.floor(step / 2)) {
    const next = at + step;
    if (next <= length && tree[next] <= left) {
    at = next;
    left -= tree[next];
    }
    }
    return Math.min(at, length - 1);
    },
    resize(next) {
    const size = Math.max(0, Math.floor(Number(next) || 0));
    const nextValues = new Float64Array(size);
    const nextKnown = new Uint8Array(size);
    const kept = Math.min(size, length);
    nextValues.set(values.subarray(0, kept));
    nextKnown.set(known.subarray(0, kept));
    for (let index = kept; index < size; index += 1) nextValues[index] = guessAt(index);
    values = nextValues;
    known = nextKnown;
    length = size;
    rebuild();
    },
    estimate(next) {
    guess = next;
    for (let index = 0; index < length; index += 1) if (!known[index]) values[index] = guessAt(index);
    rebuild();
    },
    forget() {
    known.fill(0);
    for (let index = 0; index < length; index += 1) values[index] = guessAt(index);
    rebuild();
    },
    };
    model.resize(count);
    return model;
    }
    const lists = new WeakMap();
    const ARIA_TABLE = '[role="table"], [role="grid"], [role="treegrid"]';
    const rowgroupsOf = (host) => Array.from(host.children).filter((child) => child.getAttribute('role') === 'rowgroup');
    function contentOf(scroller, given) {
    const found = given ?? (scroller.matches(ARIA_TABLE) ? scroller : scroller.querySelector(`tbody, ul, ol, [role="list"], [role="listbox"], [role="rowgroup"], ${ARIA_TABLE}`)) ?? scroller;
    if (found.localName === 'table') return found.tBodies?.[0] ?? found.createTBody?.() ?? found;
    return found.matches(ARIA_TABLE) ? rowgroupsOf(found).at(-1) ?? found : found;
    }
    const SPACER_TAG = { ul: 'li', ol: 'li', menu: 'li', table: 'tr', tbody: 'tr' };
    function create(scroller, options = {}) {
    if (!scroller?.ownerDocument) {
    return { get: () => ({ start: 0, end: 0, count: 0, active: 0 }), set() {}, on: () => () => {}, refresh() {}, scrollTo: () => null, focus: () => null, row: () => null, indexOf: () => -1, destroy() {} };
    }
    const existing = lists.get(scroller);
    if (existing) return existing;
    if (typeof options.render !== 'function') throw new TypeError('DS.virtual.create: options.render(index) returns the markup of one row');
    const doc = scroller.ownerDocument;
    const view = doc.defaultView;
    const content = contentOf(scroller, options.content);
    const onDocument = scroller === doc.scrollingElement;
    const overscan = Math.max(0, options.overscan ?? OVERSCAN);
    const keyboard = options.keyboard !== false;
    const host = content.closest('table, [role="table"], [role="grid"], [role="treegrid"]');
    const spacerTag = SPACER_TAG[content.localName] ?? 'div';
    const model = sizes(options.count ?? 0, options.estimate ?? ESTIMATE);
    let guessed = options.estimate !== undefined;
    let total = options.total ?? null;
    let active = clamp(options.active ?? 0, 0, Math.max(0, model.count - 1));
    let start = 0;
    let end = 0;
    let kept = null;
    let ownTop = null;
    let announced = '';
    let lastHtml = null;
    let frame = 0;
    let destroyed = false;
    let holder = null;
    let holderTimer = 0;
    let rendered = new Map();
    const indexOfRow = new WeakMap();
    const markup = new Map();
    const decorated = new WeakSet();
    const parked = new WeakMap();
    const rowCount = () => total ?? model.count;
    const headOf = () => host?.tHead ?? (host ? rowgroupsOf(host).find((group) => group !== content && group.compareDocumentPosition(content) & 4) : null) ?? null;
    const rowOffset = () => {
    if (options.rowOffset !== undefined) return options.rowOffset;
    const head = headOf();
    return head ? head.rows?.length ?? head.querySelectorAll('[role="row"]').length : 0;
    };
    const columns = () => {
    if (options.columns) return options.columns;
    const first = host?.tHead?.rows?.[0] ?? host?.rows?.[0];
    return first ? Array.from(first.cells).reduce((sum, cell) => sum + cell.colSpan, 0) || 1 : 1;
    };
    const topOf = () => (onDocument ? 0 : scroller.getBoundingClientRect().top + scroller.clientTop);
    const viewHeight = () => (onDocument ? view.innerHeight : scroller.clientHeight);
    const origin = () => {
    let first = content.firstElementChild;
    while (first && (first.hasAttribute('data-ds-keep') || first.hasAttribute('data-ds-leaving'))) first = first.nextElementSibling;
    return (first ?? content).getBoundingClientRect().top - topOf() + scroller.scrollTop;
    };
    const stuckHead = () => {
    const head = headOf();
    if (!head) return 0;
    const bottom = head.getBoundingClientRect().height;
    for (const part of [head, ...head.querySelectorAll('tr, th, td, [role="row"], [role="columnheader"]')]) {
    const style = view.getComputedStyle(part);
    if (style.position === 'sticky') return bottom + (parseFloat(style.insetBlockStart) || 0);
    }
    return 0;
    };
    const insets = () => {
    const style = view.getComputedStyle(scroller);
    return [Math.max(parseFloat(style.scrollPaddingBlockStart) || 0, stuckHead()), parseFloat(style.scrollPaddingBlockEnd) || 0];
    };
    const windowNow = () => {
    if (!model.count) return [0, 0];
    const height = viewHeight();
    const from = Math.max(0, scroller.scrollTop - origin());
    const first = model.indexAt(from);
    if (height <= 0) return [first, Math.min(model.count, first + BLIND_ROWS)];
    const last = model.indexAt(from + height);
    return [Math.max(0, first - overscan), Math.min(model.count, last + 1 + overscan)];
    };
    const spacer = (place, px) => {
    const size = `height:${px}px`;
    if (spacerTag === 'tr') return `<tr data-ds-key="ds-virtual-gap-${place}" data-ds-virtual-gap aria-hidden="true" style="${size};visibility:hidden"><td colspan="${columns()}" style="${size};padding:0;border:0"></td></tr>`;
    return `<${spacerTag} data-ds-key="ds-virtual-gap-${place}" data-ds-virtual-gap aria-hidden="true" style="${size};margin:0;padding:0;border:0;visibility:hidden;list-style:none"></${spacerTag}>`;
    };
    const rowMarkup = (index) => {
    if (!markup.has(index)) markup.set(index, String(options.render(index)));
    return markup.get(index);
    };
    const compose = (indices) => {
    let html = '';
    let cursor = 0;
    let place = 0;
    for (const index of indices) {
    if (index > cursor) {
    html += spacer(place, model.offset(index) - model.offset(cursor));
    place += 1;
    }
    html += rowMarkup(index);
    cursor = index + 1;
    }
    if (cursor < model.count) html += spacer(place, model.total() - model.offset(cursor));
    return html;
    };
    const releaseControls = (row) => {
    for (const control of row.querySelectorAll(ROW_STOP)) {
    if (!parked.has(control)) continue;
    const before = parked.get(control);
    parked.delete(control);
    if (before === null) control.removeAttribute('tabindex');
    else control.setAttribute('tabindex', before);
    disown(control, 'attrs', 'tabindex');
    }
    };
    const setStop = (row, isActive) => {
    setAttribute(row, 'tabindex', isActive ? '0' : '-1');
    if (isActive) {
    releaseControls(row);
    return;
    }
    for (const control of row.querySelectorAll(ROW_STOP)) {
    const value = control.getAttribute('tabindex');
    if (parked.has(control) || Number(value) < 0) continue;
    parked.set(control, value);
    own(control, 'attrs', 'tabindex');
    control.setAttribute('tabindex', '-1');
    }
    };
    const decorate = (row, index) => {
    if (!decorated.has(row)) {
    decorated.add(row);
    for (const name of host ? ['aria-rowindex'] : ['aria-setsize', 'aria-posinset']) own(row, 'attrs', name);
    if (keyboard) own(row, 'attrs', 'tabindex');
    }
    if (host) setAttribute(row, 'aria-rowindex', String(rowOffset() + index + 1));
    else {
    setAttribute(row, 'aria-setsize', String(rowCount()));
    setAttribute(row, 'aria-posinset', String(index + 1));
    }
    if (keyboard) setStop(row, index === active);
    };
    const write = (html, indices) => {
    const top = scroller.scrollTop;
    morphHtml(content, html);
    if (scroller.scrollTop !== top) scroller.scrollTop = top;
    ownTop = scroller.scrollTop;
    watcher?.takeRecords();
    lastHtml = html;
    const next = new Map();
    const rows = Array.from(content.children).filter((child) => !child.hasAttribute('data-ds-virtual-gap') && !child.hasAttribute('data-ds-keep') && !child.hasAttribute('data-ds-leaving'));
    if (rows.length !== indices.length) throw new Error(`DS.virtual: render(index) must return one root element per row (${indices.length} rows, ${rows.length} elements)`);
    rows.forEach((row, at) => {
    const index = indices[at];
    next.set(index, row);
    indexOfRow.set(row, index);
    decorate(row, index);
    if (!rendered.has(index) || rendered.get(index) !== row) resizes?.observe(row);
    });
    const kept = new Set(next.values());
    for (const row of rendered.values()) if (!kept.has(row)) resizes?.unobserve(row);
    rendered = next;
    for (const index of [...markup.keys()]) if (!next.has(index)) markup.delete(index);
    };
    const measure = () => {
    if (!rendered.size) return false;
    if (!guessed && options.estimate === undefined) {
    const first = rendered.values().next().value;
    const height = first.getBoundingClientRect().height;
    if (height > 0) {
    guessed = true;
    model.estimate(height);
    }
    }
    const from = Math.max(0, scroller.scrollTop - origin());
    let shift = 0;
    let changed = false;
    const order = [...rendered.keys()].sort((a, b) => a - b);
    for (const index of order) {
    const height = rendered.get(index).getBoundingClientRect().height;
    const before = model.size(index);
    if (height <= 0 || (Math.abs(height - before) < EPSILON && model.measured(index))) {
    if (height > 0) model.set(index, height);
    continue;
    }
    if (model.offset(index) + before <= from + shift) shift += height - before;
    model.set(index, height);
    changed = true;
    }
    if (Math.abs(shift) >= EPSILON) {
    scroller.scrollTop += shift;
    ownTop = scroller.scrollTop;
    }
    return changed;
    };
    const restoreFocus = () => {
    if (!holder || holder.isConnected) return;
    holder = null;
    if (!keyboard) return;
    const row = rendered.get(active);
    if (row) {
    row.focus({ preventScroll: true });
    return;
    }
    const lent = !scroller.hasAttribute('tabindex');
    if (lent) scroller.setAttribute('tabindex', '-1');
    scroller.focus({ preventScroll: true });
    if (lent) doc.addEventListener('focusin', () => scroller.removeAttribute('tabindex'), { once: true });
    };
    let remeasured = false;
    let revealing = false;
    const update = () => {
    if (destroyed) return;
    active = clamp(active, 0, Math.max(0, model.count - 1));
    for (let pass = 0; pass < MAX_PASSES; pass += 1) {
    [start, end] = windowNow();
    const indices = [];
    for (let index = start; index < end; index += 1) indices.push(index);
    if (model.count && (active < start || active >= end)) {
    if (active < start) indices.unshift(active);
    else indices.push(active);
    }
    const html = compose(indices);
    const wrote = html !== lastHtml;
    if (wrote) write(html, indices);
    const moved = measure();
    if (moved) remeasured = true;
    if (!wrote && !moved) break;
    }
    if (remeasured && kept && !revealing && kept.index < model.count) {
    remeasured = false;
    revealing = true;
    try {
    reveal(kept.index, kept.align);
    } finally {
    revealing = false;
    }
    }
    remeasured = false;
    restoreFocus();
    const key = `${start}:${end}:${model.count}`;
    if (key !== announced) {
    announced = key;
    emit(scroller, 'range', { start, end, count: model.count });
    }
    };
    const schedule = () => {
    if (frame || destroyed) return;
    frame = view.requestAnimationFrame(() => {
    frame = 0;
    update();
    });
    };
    const setActive = (index) => {
    const next = clamp(index, 0, Math.max(0, model.count - 1));
    if (next === active) return;
    const before = rendered.get(active);
    active = next;
    if (before && keyboard) setStop(before, false);
    const row = rendered.get(active);
    if (row && keyboard) setStop(row, true);
    emit(scroller, 'active', { index: active, element: row ?? null });
    };
    const rowOf = (node) => {
    let at = node?.nodeType === 1 ? node : node?.parentElement;
    while (at && at.parentElement !== content) at = at.parentElement;
    return at && indexOfRow.has(at) ? at : null;
    };
    const scrollTo = (index, { align = 'auto' } = {}) => {
    if (!model.count) return null;
    const target = clamp(Math.trunc(index) || 0, 0, model.count - 1);
    kept = null;
    update();
    reveal(target, align);
    kept = { index: target, align };
    return rendered.get(target) ?? null;
    };
    const reveal = (target, align) => {
    for (let pass = 0; pass < MAX_PASSES; pass += 1) {
    const [insetStart, insetEnd] = insets();
    const top = scroller.scrollTop;
    const height = viewHeight();
    const row = rendered.get(target);
    const box = row?.getBoundingClientRect();
    const rowTop = box && box.height > 0 ? box.top - topOf() + scroller.scrollTop : origin() + model.offset(target);
    const rowEnd = rowTop + (box && box.height > 0 ? box.height : model.size(target));
    let goal = top;
    if (align === 'start') goal = rowTop - insetStart;
    else if (align === 'end') goal = rowEnd - height + insetEnd;
    else if (align === 'center') goal = rowTop - (height - model.size(target)) / 2;
    else if (rowTop < top + insetStart) goal = rowTop - insetStart;
    else if (rowEnd > top + height - insetEnd) goal = rowEnd - height + insetEnd;
    if (Math.abs(goal - top) < 1) break;
    scroller.scrollTop = goal;
    ownTop = scroller.scrollTop;
    update();
    if (Math.abs(scroller.scrollTop - top) < 1) break;
    }
    };
    const focusRow = (index) => {
    if (!model.count) return null;
    const target = clamp(Math.trunc(index) || 0, 0, model.count - 1);
    setActive(target);
    const row = scrollTo(target);
    (row ?? rendered.get(target))?.focus({ preventScroll: true });
    return row ?? null;
    };
    const onKey = ( event) => {
    if (!keyboard) return;
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (event.target?.matches?.(TEXT_ENTRY)) return;
    const row = rowOf(event.target);
    if (!row) return;
    const from = indexOfRow.get(row);
    const page = Math.max(1, model.indexAt(scroller.scrollTop - origin() + viewHeight()) - model.indexAt(Math.max(0, scroller.scrollTop - origin())));
    const to = { ArrowDown: from + 1, ArrowUp: from - 1, Home: 0, End: model.count - 1, PageDown: from + page, PageUp: from - page }[event.key];
    if (to === undefined) return;
    event.preventDefault();
    focusRow(to);
    };
    const onFocusIn = ( event) => {
    clearTimeout(holderTimer);
    holder = (event.target);
    const row = rowOf(event.target);
    if (row) setActive(indexOfRow.get(row));
    };
    const onFocusOut = () => {
    clearTimeout(holderTimer);
    holderTimer = setTimeout(() => {
    holder = null;
    });
    };
    const resizes = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(schedule) : null;
    const watcher = typeof view.MutationObserver === 'function' ? new view.MutationObserver((records) => {
    if (!records.length || destroyed) return;
    lastHtml = null;
    update();
    }) : null;
    own(scroller, 'props', 'overflow-anchor');
    const anchorBefore = scroller.style.overflowAnchor;
    scroller.style.overflowAnchor = 'none';
    const scrollerStop = keyboard && !onDocument && !scroller.hasAttribute('tabindex');
    if (scrollerStop) {
    own(scroller, 'attrs', 'tabindex');
    scroller.setAttribute('tabindex', '-1');
    }
    const rowcountBefore = host?.getAttribute('aria-rowcount') ?? null;
    if (host) {
    own(host, 'attrs', 'aria-rowcount');
    setAttribute(host, 'aria-rowcount', String(rowCount() < 0 ? -1 : rowCount() + rowOffset()));
    }
    const scrollTarget = onDocument ? view : scroller;
    const onScroll = () => {
    if (kept && ownTop !== null && Math.abs(scroller.scrollTop - ownTop) >= 1) kept = null;
    schedule();
    };
    scrollTarget.addEventListener('scroll', onScroll, { passive: true });
    const release = () => { kept = null; };
    for (const type of TAKE_OVER) scrollTarget.addEventListener(type, release, { passive: true });
    if (onDocument) view.addEventListener('resize', schedule);
    content.addEventListener('keydown', onKey);
    content.addEventListener('focusin', onFocusIn);
    content.addEventListener('focusout', onFocusOut);
    resizes?.observe(scroller);
    watcher?.observe(content, { childList: true });
    const controller = {
    get: () => ({ start, end, count: model.count, active }),
    set(value = {}) {
    if ((value.count !== undefined && value.count !== model.count) || value.forget) kept = null;
    if (value.count !== undefined) model.resize(value.count);
    if (value.total !== undefined) total = value.total;
    if (value.forget) {
    model.forget();
    markup.clear();
    }
    if (host) setAttribute(host, 'aria-rowcount', String(rowCount() < 0 ? -1 : rowCount() + rowOffset()));
    if (value.active !== undefined) setActive(value.active);
    for (const [index, row] of rendered) decorate(row, index);
    update();
    },
    on(type, fn) {
    const listener = ( event) => {
    if (event.target === scroller) fn(event.detail);
    };
    scroller.addEventListener(`ds:${type}`, listener);
    return () => scroller.removeEventListener(`ds:${type}`, listener);
    },
    refresh(index) {
    if (index === undefined) markup.clear();
    else markup.delete(index);
    lastHtml = null;
    update();
    },
    scrollTo,
    focus: focusRow,
    row: (index) => rendered.get(index) ?? null,
    indexOf(node) {
    const row = rowOf(node);
    return row ? indexOfRow.get(row) : -1;
    },
    destroy() {
    if (destroyed) return;
    destroyed = true;
    lists.delete(scroller);
    if (frame) view.cancelAnimationFrame(frame);
    clearTimeout(holderTimer);
    scrollTarget.removeEventListener('scroll', onScroll);
    for (const type of TAKE_OVER) scrollTarget.removeEventListener(type, release);
    if (onDocument) view.removeEventListener('resize', schedule);
    content.removeEventListener('keydown', onKey);
    content.removeEventListener('focusin', onFocusIn);
    content.removeEventListener('focusout', onFocusOut);
    resizes?.disconnect();
    watcher?.disconnect();
    scroller.style.overflowAnchor = anchorBefore;
    disown(scroller, 'props', 'overflow-anchor');
    if (scrollerStop) {
    scroller.removeAttribute('tabindex');
    disown(scroller, 'attrs', 'tabindex');
    }
    if (host) {
    if (rowcountBefore === null) host.removeAttribute('aria-rowcount');
    else host.setAttribute('aria-rowcount', rowcountBefore);
    disown(host, 'attrs', 'aria-rowcount');
    }
    for (const row of rendered.values()) {
    for (const name of host ? ['aria-rowindex'] : ['aria-setsize', 'aria-posinset']) {
    row.removeAttribute(name);
    disown(row, 'attrs', name);
    }
    if (keyboard) {
    releaseControls(row);
    row.removeAttribute('tabindex');
    disown(row, 'attrs', 'tabindex');
    }
    }
    for (const gap of Array.from(content.children)) if (gap.hasAttribute('data-ds-virtual-gap')) gap.remove();
    },
    };
    lists.set(scroller, controller);
    update();
    return controller;
    }

    return Object.freeze({
      sizes: sizes,
      create: create
    });
  });

  add('dnd', ['create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const emit = DS.core.emit, runtime = DS.core.runtime, t = DS.core.t;
    const flip = DS.motion.flip, motionDuration = DS.motion.motionDuration, motionEasing = DS.motion.motionEasing, travelDuration = DS.motion.travelDuration;
    const DRAG_THRESHOLD = 4;
    const SCROLL_EDGE = 48;
    const SCROLL_SPEED = 16;
    const LABEL_LENGTH = 60;
    const INTERACTIVE = 'a[href], button, input, select, textarea, label, [contenteditable]:not([contenteditable="false"])';
    const MOVE_KEYS = new Set(['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End']);
    const shared = runtime.state('dnd', () => ({
    regions: new WeakMap(),
    hints: new WeakMap(),
    count: 0,
    }));
    const OWNED = Symbol.for('ds.owned');
    function own(element, kind, name) {
    const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
    record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
    }
    function disown(element, kind, name) {
    const map = element[OWNED]?.[kind];
    const count = map?.get(name) ?? 0;
    if (count > 1) map.set(name, count - 1);
    else map?.delete(name);
    }
    const speechHost = (node) => node.closest?.('dialog, .ds-dialog, .ds-sheet') ?? node.closest?.('.ds-theme') ?? node.ownerDocument?.body;
    function regionOf(node) {
    const host = speechHost(node);
    if (!host) return null;
    let region = shared.regions.get(host);
    if (!region?.isConnected) {
    region = node.ownerDocument.createElement('div');
    region.className = 'ds-sr';
    region.setAttribute('aria-live', 'polite');
    region.setAttribute('data-ds-keep', '');
    host.append(region);
    shared.regions.set(host, region);
    }
    return region;
    }
    function speak(node, text) {
    const region = text ? regionOf(node) : null;
    if (!region) return;
    region.textContent = '';
    setTimeout(() => {
    region.textContent = text;
    });
    }
    function hintId(node) {
    const host = speechHost(node);
    if (!host) return '';
    let hint = shared.hints.get(host);
    if (!hint?.isConnected) {
    hint = node.ownerDocument.createElement('div');
    shared.count += 1;
    hint.id = `ds-dnd-hint-${shared.count}`;
    hint.hidden = true;
    hint.setAttribute('data-ds-keep', '');
    host.append(hint);
    shared.hints.set(host, hint);
    }
    hint.textContent = t('dragHint');
    return hint.id;
    }
    function layoutBox(element) {
    const parent = element.offsetParent;
    const width = element.offsetWidth ?? 0;
    const height = element.offsetHeight ?? 0;
    if (!parent) {
    const rect = element.getBoundingClientRect();
    return { left: rect.left, top: rect.top, right: rect.left + width, bottom: rect.top + height, width, height };
    }
    const doc = element.ownerDocument;
    const view = doc.defaultView;
    let left;
    let top;
    if (parent === doc.body && view.getComputedStyle(parent).position === 'static') {
    const own = parent === doc.scrollingElement ? 0 : 1;
    left = element.offsetLeft - view.scrollX - own * parent.scrollLeft;
    top = element.offsetTop - view.scrollY - own * parent.scrollTop;
    } else {
    const rect = parent.getBoundingClientRect();
    left = rect.left + parent.clientLeft - parent.scrollLeft + element.offsetLeft;
    top = rect.top + parent.clientTop - parent.scrollTop + element.offsetTop;
    }
    for (let at = element.parentElement; at && at !== parent; at = at.parentElement) {
    left -= at.scrollLeft;
    top -= at.scrollTop;
    }
    return { left, top, right: left + width, bottom: top + height, width, height };
    }
    const scrollerOverflow = /auto|scroll|overlay/;
    function scrollers(item) {
    const found = [];
    const doc = item.ownerDocument;
    const translate = item.style.translate;
    item.style.removeProperty('translate');
    const limits = (element) => ({ element, maxTop: element.scrollHeight - element.clientHeight, maxLeft: element.scrollWidth - element.clientWidth });
    for (let at = item.parentElement; at && at !== doc.documentElement; at = at.parentElement) {
    const style = doc.defaultView.getComputedStyle(at);
    if ((scrollerOverflow.test(style.overflowY) && at.scrollHeight > at.clientHeight) || (scrollerOverflow.test(style.overflowX) && at.scrollWidth > at.clientWidth)) found.push(limits(at));
    }
    if (doc.scrollingElement) found.push(limits(doc.scrollingElement));
    item.style.translate = translate;
    return found;
    }
    const sameTarget = (a, b) => a === b || (Boolean(a) && Boolean(b) && a.zone === b.zone && a.index === b.index && a.position === b.position);
    const instances = new WeakMap();
    function create(container, options = {}) {
    if (!container?.ownerDocument) return { get: () => null, set: () => false, cancel() {}, on: () => () => {}, destroy() {} };
    const existing = instances.get(container);
    if (existing) return existing;
    const doc = container.ownerDocument;
    const view = doc.defaultView;
    const handleSelector = options.handle ?? '.ds-grip';
    const selector = options.items;
    const axis = options.axis ?? 'block';
    const autoScroll = options.autoScroll !== false;
    const allZones = () => {
    if (!options.zones) return [container];
    const found = Array.from(container.querySelectorAll(options.zones));
    return container.matches(options.zones) ? [container, ...found] : found;
    };
    const severalZones = () => Boolean(options.zones) && allZones().length > 1;
    const zonesFor = (item) => allZones().filter((zone) => !item.contains(zone));
    const accepts = (item, zone) => (options.accept ? options.accept(item, zone) !== false : true);
    const zoneOf = (node) => {
    const zones = options.zones;
    if (!zones) return container;
    const zone = node.parentElement?.closest(zones);
    return zone && container.contains(zone) ? zone : null;
    };
    const listOf = (zone) => (options.list ? zone.querySelector(options.list) ?? zone : zone);
    const itemsIn = (zone) => {
    const list = selector ? Array.from(zone.querySelectorAll(selector)) : Array.from(listOf(zone).children);
    return list.filter((item) => !item.hasAttribute('data-ds-keep') && !item.hasAttribute('data-ds-leaving') && !item.hidden && zoneOf(item) === zone);
    };
    const itemOf = (node) => {
    const zones = allZones();
    for (let at = node; at && at !== container.parentElement; at = at.parentElement) {
    const zone = zoneOf(at);
    if (zone && zones.includes(zone) && (selector ? at.matches(selector) : at.parentElement === listOf(zone))) return at;
    }
    return null;
    };
    const blocked = (handle, item) => [handle, item].some((node) => node.matches(':disabled') || node.getAttribute('aria-disabled') === 'true' || node.getAttribute('aria-busy') === 'true');
    const labelOf = (item) => {
    if (options.label) return String(options.label(item) ?? '');
    const text = (item.getAttribute('aria-label') ?? item.textContent ?? '').replace(/\s+/g, ' ').trim();
    return text.length > LABEL_LENGTH ? `${text.slice(0, LABEL_LENGTH)}…` : text;
    };
    const zoneLabelOf = (zone) => {
    if (!zone) return '';
    if (options.zoneLabel) return String(options.zoneLabel(zone) ?? '');
    const named = zone.getAttribute('aria-labelledby');
    return (zone.getAttribute('aria-label') ?? (named ? doc.getElementById(named)?.textContent : '') ?? '').replace(/\s+/g, ' ').trim();
    };
    const say = (type, item, target) => {
    const zone = target?.zone ?? null;
    const others = zone ? itemsIn(zone).filter((other) => other !== item) : [];
    const detail = {
    item,
    zone,
    label: labelOf(item),
    zoneName: severalZones() ? zoneLabelOf(zone) : '',
    position: (target?.index ?? 0) + 1,
    total: others.length + 1,
    };
    const key = { grab: 'dragGrabbed', move: detail.zoneName ? 'dragMovedZone' : 'dragMoved', drop: detail.zoneName ? 'dragDroppedZone' : 'dragDropped', cancel: 'dragCancelled' }[type];
    const text = options.announce ? options.announce(type, detail) : t(key, { label: detail.label, zone: detail.zoneName, position: detail.position, total: detail.total });
    speak(container, text);
    };
    const whereIs = (item) => {
    const zone = zoneOf(item);
    return { zone, index: zone ? Math.max(0, itemsIn(zone).indexOf(item)) : 0 };
    };
    const put = (item, zone, index) => {
    const others = itemsIn(zone).filter((other) => other !== item);
    const anchor = others[index] ?? null;
    const parent = anchor?.parentNode ?? others.at(-1)?.parentNode ?? listOf(zone);
    const reference = anchor ?? others.at(-1)?.nextSibling ?? null;
    const focused = item.getRootNode().activeElement;
    const keep = focused && item.contains(focused) ? focused : null;
    const scrolled = [];
    for (let at = parent; at; at = at.parentElement) scrolled.push([at, at.scrollTop, at.scrollLeft]);
    if (typeof parent.moveBefore === 'function') {
    try {
    parent.moveBefore(item, reference);
    } catch {
    parent.insertBefore(item, reference);
    }
    } else parent.insertBefore(item, reference);
    if (keep && item.getRootNode().activeElement !== keep) keep.focus({ preventScroll: true });
    for (const [at, top, left] of scrolled) {
    if (at.scrollTop !== top) at.scrollTop = top;
    if (at.scrollLeft !== left) at.scrollLeft = left;
    }
    };
    const glideHome = (item, from) => {
    const to = item.getBoundingClientRect();
    const dx = from.left - to.left;
    const dy = from.top - to.top;
    if (Math.hypot(dx, dy) < 0.5 || typeof item.animate !== 'function') return;
    const glide = item.animate([{ translate: `${dx}px ${dy}px` }, { translate: '0px 0px' }], {
    duration: travelDuration(container, Math.hypot(dx, dy)),
    easing: motionEasing(container, '--ds-ease-spatial'),
    });
    glide.id = 'ds-drag';
    };
    const defaultPlace = (target, context) => {
    const { item, mode } = context;
    const others = selector ? `:is(${selector}):not([data-ds-dragging]):not([data-ds-dragging] *)` : ':scope > :not([data-ds-dragging])';
    const marked = !item.hasAttribute('data-ds-dragging');
    if (marked) item.setAttribute('data-ds-dragging', 'end');
    const showing = item.getBoundingClientRect();
    for (const animation of item.getAnimations?.() ?? []) if (animation.id === 'ds-drag') animation.cancel();
    const zones = [...new Set([zoneOf(item), target.zone])].filter(Boolean);
    const run = (at) => {
    if (at === zones.length) put(item, target.zone, target.index);
    else flip(selector ? zones[at] : listOf(zones[at]), () => run(at + 1), { items: others });
    };
    run(0);
    if (marked) item.removeAttribute('data-ds-dragging');
    if (mode === 'keyboard') item.scrollIntoView?.({ block: 'nearest', inline: 'nearest', behavior: 'instant' });
    if (mode !== 'pointer') glideHome(item, showing);
    };
    let drag = null;
    let press = null;
    const ending = new Map();
    const hinted = new Map();
    let clickGuard = null;
    const letClickGo = () => {
    clickGuard?.();
    clickGuard = null;
    };
    const contextOf = () => ({ item: drag.item, mode: drag.mode, from: drag.from, current: drag.current, zones: zonesFor(drag.item), itemsIn, accepts, layoutBox });
    const setDragover = (zone) => {
    if (drag.over === zone) return;
    if (drag.over) {
    drag.over.removeAttribute('data-ds-dragover');
    disown(drag.over, 'attrs', 'data-ds-dragover');
    }
    drag.over = zone && severalZones() ? zone : null;
    if (drag.over) {
    own(drag.over, 'attrs', 'data-ds-dragover');
    drag.over.setAttribute('data-ds-dragover', '');
    }
    };
    const finish = (item) => {
    const end = ending.get(item);
    if (!end) return;
    ending.delete(item);
    clearTimeout(end.timer);
    item.removeAttribute('data-ds-dragging');
    item.style.zIndex = end.z;
    if (!item.style.length) item.removeAttribute('style');
    disown(item, 'attrs', 'data-ds-dragging');
    for (const name of ['translate', 'z-index']) disown(item, 'props', name);
    };
    const keepCapture = () => {
    if (!drag || drag.pointer === null) return true;
    try {
    if (!drag.handle.hasPointerCapture?.(drag.pointer)) drag.handle.setPointerCapture?.(drag.pointer);
    return true;
    } catch {
    return false;
    }
    };
    const moveTo = (target) => {
    if (!target || sameTarget(target, drag.current)) return false;
    if (target.zone && (drag.item.contains(target.zone) || !accepts(drag.item, target.zone))) return false;
    const zoneBefore = drag.current.zone;
    drag.moving = true;
    try {
    (options.place ?? defaultPlace)(target, contextOf());
    } finally {
    drag.moving = false;
    }
    drag.current = target;
    keepCapture();
    setDragover(target.zone);
    say('move', drag.item, target);
    emit(container, 'drag-move', { item: drag.item, from: drag.from, to: target, mode: drag.mode });
    if (autoScroll && drag.mode === 'pointer' && target.zone !== zoneBefore) drag.scrollers = scrollers(drag.item);
    return true;
    };
    const defaultTarget = (point, context) => {
    const { item, current } = context;
    const zones = context.zones;
    const hit = zones.filter((zone) => {
    const rect = zone.getBoundingClientRect();
    return point.x >= rect.left && point.x <= rect.right && point.y >= rect.top && point.y <= rect.bottom;
    }).filter((zone) => accepts(item, zone));
    const zone = hit.find((candidate) => !hit.some((other) => other !== candidate && candidate.contains(other))) ?? current.zone;
    const others = itemsIn(zone).filter((other) => other !== item);
    const box = { left: point.x - drag.grabX, top: point.y - drag.grabY, width: drag.width, height: drag.height };
    const inline = axis === 'inline';
    const start = inline ? box.left : box.top;
    const end = start + (inline ? box.width : box.height);
    const middle = (other) => {
    const layout = layoutBox(other);
    return inline ? layout.left + layout.width / 2 : layout.top + layout.height / 2;
    };
    if (axis === 'both') {
    const cx = box.left + box.width / 2;
    const cy = box.top + box.height / 2;
    const over = others.findIndex((other) => {
    const layout = layoutBox(other);
    return cx >= layout.left && cx <= layout.right && cy >= layout.top && cy <= layout.bottom;
    });
    if (over < 0) return zone === current.zone ? current : { zone, index: others.length };
    const index = zone === current.zone && current.index > over ? over : over + (zone === current.zone ? 1 : 0);
    return { zone, index: Math.min(index, others.length) };
    }
    if (zone !== current.zone) {
    const centre = (start + end) / 2;
    const index = others.findIndex((other) => centre < middle(other));
    return { zone, index: index < 0 ? others.length : index };
    }
    let index = Math.min(current.index, others.length);
    while (index > 0 && start < middle(others[index - 1])) index -= 1;
    while (index < others.length && end > middle(others[index])) index += 1;
    return { zone, index };
    };
    const follow = () => {
    const { item } = drag;
    const layout = layoutBox(item);
    const constrained = axis !== 'both' && !severalZones() && !options.target;
    const dx = drag.x - drag.grabX - layout.left;
    const dy = drag.y - drag.grabY - layout.top;
    const x = constrained && axis === 'block' ? 0 : dx;
    const y = constrained && axis === 'inline' ? 0 : dy;
    item.style.translate = `${Math.round(x * 10) / 10}px ${Math.round(y * 10) / 10}px`;
    };
    const track = () => {
    const target = (options.target ?? defaultTarget)({ x: drag.x, y: drag.y }, contextOf());
    moveTo(target);
    follow();
    };
    const scrollFrame = () => {
    if (!drag || drag.mode !== 'pointer') return;
    drag.frame = view.requestAnimationFrame(scrollFrame);
    for (const { element: scroller, maxTop, maxLeft } of drag.scrollers) {
    const page = scroller === doc.scrollingElement;
    const rect = page ? { top: 0, left: 0, bottom: view.innerHeight, right: view.innerWidth } : scroller.getBoundingClientRect();
    const step = (near, far) => {
    if (near < SCROLL_EDGE) return -SCROLL_SPEED * (1 - Math.max(0, near) / SCROLL_EDGE);
    if (far < SCROLL_EDGE) return SCROLL_SPEED * (1 - Math.max(0, far) / SCROLL_EDGE);
    return 0;
    };
    const dy = step(drag.y - rect.top, rect.bottom - drag.y);
    const dx = step(drag.x - rect.left, rect.right - drag.x);
    const beforeTop = scroller.scrollTop;
    const beforeLeft = scroller.scrollLeft;
    if (dy) scroller.scrollTop = Math.min(maxTop, beforeTop + dy);
    if (dx) scroller.scrollLeft = Math.min(maxLeft, beforeLeft + dx);
    if (scroller.scrollTop !== beforeTop || scroller.scrollLeft !== beforeLeft) {
    track();
    return;
    }
    }
    };
    const onDragKey = ( event) => {
    if (!drag) return;
    if (drag.mode === 'keyboard' && (!drag.handle.isConnected || drag.handle.getRootNode().activeElement !== drag.handle)) {
    end(false);
    return;
    }
    if (event.key === 'Escape') {
    event.preventDefault();
    event.stopPropagation();
    end(false);
    return;
    }
    if (drag.mode !== 'keyboard' || event.altKey || event.ctrlKey || event.metaKey) return;
    if (event.key === ' ' || event.key === 'Enter') {
    event.preventDefault();
    event.stopPropagation();
    if (!event.repeat) end(true);
    return;
    }
    if (!MOVE_KEYS.has(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    const context = contextOf();
    const target = (options.keyTarget ?? defaultKeyTarget)(event.key, context);
    if (moveTo(target) && options.place) drag.item.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
    };
    const defaultKeyTarget = (key, context) => {
    const { item, current } = context;
    const others = itemsIn(current.zone).filter((other) => other !== item);
    if (key === 'Home') return { zone: current.zone, index: 0 };
    if (key === 'End') return { zone: current.zone, index: others.length };
    if (axis === 'both') {
    if (key === 'ArrowLeft' || key === 'ArrowRight') {
    const index = current.index + (key === 'ArrowLeft' ? -1 : 1);
    if (index >= 0 && index <= others.length) return { zone: current.zone, index };
    } else {
    const box = layoutBox(item);
    const centre = box.left + box.width / 2;
    let best = null;
    for (const [index, other] of others.entries()) {
    const layout = layoutBox(other);
    if (key === 'ArrowUp' ? layout.bottom > box.top : layout.top < box.bottom) continue;
    const distance = Math.abs(layout.top - box.top) * 1000 + Math.abs(layout.left + layout.width / 2 - centre);
    if (!best || distance < best.distance) best = { index, distance };
    }
    if (best) return { zone: current.zone, index: key === 'ArrowDown' ? Math.min(best.index + 1, others.length) : best.index };
    }
    } else {
    const along = axis === 'inline' ? ['ArrowLeft', 'ArrowRight'] : ['ArrowUp', 'ArrowDown'];
    if (along.includes(key)) return { zone: current.zone, index: clampIndex(current.index + (key === along[0] ? -1 : 1), others.length) };
    }
    const zones = context.zones;
    const step = key === 'ArrowLeft' || key === 'ArrowUp' ? -1 : 1;
    for (let at = zones.indexOf(current.zone) + step; at >= 0 && at < zones.length; at += step) {
    if (!accepts(item, zones[at])) continue;
    const count = itemsIn(zones[at]).length;
    return { zone: zones[at], index: Math.min(current.index, count) };
    }
    return null;
    };
    const clampIndex = (index, last) => Math.min(Math.max(index, 0), last);
    const lift = (item, handle, mode, fields = {}) => {
    finish(item);
    const from = whereIs(item);
    if (mode === 'pointer') for (const animation of item.getAnimations?.() ?? []) if (animation.id === 'ds-drag') animation.cancel();
    if (!emit(container, 'drag-start', { item, from, mode }, { cancelable: true })) return false;
    drag = { item, handle, mode, from, current: from, x: 0, y: 0, grabX: 0, grabY: 0, width: 0, height: 0, pointer: null, over: null, frame: 0, moving: false, z: item.style.zIndex, scrollers: autoScroll && mode === 'pointer' ? scrollers(item) : [], ...fields };
    own(item, 'attrs', 'data-ds-dragging');
    for (const name of ['translate', 'z-index']) own(item, 'props', name);
    item.setAttribute('data-ds-dragging', '');
    const sticky = parseInt(view.getComputedStyle(container).getPropertyValue('--ds-z-sticky'), 10);
    item.style.zIndex = Number.isFinite(sticky) ? String(sticky - 1) : 'calc(var(--ds-z-sticky) - 1)';
    setDragover(from.zone);
    doc.addEventListener('keydown', onDragKey, true);
    doc.addEventListener('lostpointercapture', onLostCapture, true);
    if (mode === 'keyboard' && typeof view.MutationObserver === 'function') {
    drag.watcher = new view.MutationObserver(() => {
    if (drag?.handle === handle && !handle.isConnected) end(false);
    });
    drag.watcher.observe(container, { childList: true, subtree: true });
    }
    say('grab', item, from);
    if (mode === 'pointer' && autoScroll) drag.frame = view.requestAnimationFrame(scrollFrame);
    return true;
    };
    const eatClick = (released) => {
    letClickGo();
    let timer = 0;
    const eat = ( event) => {
    if (!event.detail) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    letClickGo();
    };
    const arm = () => {
    timer = setTimeout(letClickGo, motionDuration(container, '--ds-dur-fast'));
    };
    doc.addEventListener('click', eat, true);
    doc.addEventListener('pointerdown', letClickGo, true);
    doc.addEventListener('pointercancel', letClickGo, true);
    if (released) arm();
    else doc.addEventListener('pointerup', arm, { capture: true, once: true });
    clickGuard = () => {
    clearTimeout(timer);
    doc.removeEventListener('click', eat, true);
    doc.removeEventListener('pointerdown', letClickGo, true);
    doc.removeEventListener('pointercancel', letClickGo, true);
    doc.removeEventListener('pointerup', arm, true);
    };
    };
    const end = (commit, release) => {
    const ended = drag;
    if (!ended) return;
    drag = null;
    const { item, handle, from, mode, z } = ended;
    if (ended.frame) view.cancelAnimationFrame(ended.frame);
    ended.watcher?.disconnect();
    if (mode === 'pointer' && release !== 'gone') eatClick(release === 'up');
    doc.removeEventListener('keydown', onDragKey, true);
    doc.removeEventListener('lostpointercapture', onLostCapture, true);
    if (ended.pointer !== null) {
    try {
    handle.releasePointerCapture?.(ended.pointer);
    } catch {
    }
    }
    const visual = item.getBoundingClientRect();
    item.style.removeProperty('translate');
    const changed = !sameTarget(ended.current, from);
    let to = ended.current;
    let dropped = commit;
    if (commit && changed) dropped = emit(container, 'drop', { item, from, to, mode }, { cancelable: true });
    if (ended.over) {
    ended.over.removeAttribute('data-ds-dragover');
    disown(ended.over, 'attrs', 'data-ds-dragover');
    }
    if (!dropped && changed && item.isConnected) {
    (options.place ?? defaultPlace)(from, { item, mode, from, current: ended.current, zones: zonesFor(item), itemsIn, accepts, layoutBox });
    to = from;
    }
    if (!commit || !dropped || !changed) emit(container, 'drag-cancel', { item, from, mode });
    say(commit && dropped ? 'drop' : 'cancel', item, to);
    glideHome(item, visual);
    item.setAttribute('data-ds-dragging', 'end');
    const timer = setTimeout(() => finish(item), Math.max(motionDuration(item, '--ds-dur-instant'), motionDuration(container, '--ds-dur-medium')));
    ending.set(item, { timer, z });
    };
    const onPointerDown = ( event) => {
    if (drag || event.button !== 0) return;
    const handle = event.target?.closest?.(handleSelector);
    if (!handle || !container.contains(handle)) return;
    const item = itemOf(handle);
    if (!item || blocked(handle, item)) return;
    const inner = event.target.closest(INTERACTIVE);
    if (inner && inner !== handle && handle.contains(inner)) return;
    handle.setPointerCapture?.(event.pointerId);
    press = { item, handle, pointer: event.pointerId, x: event.clientX, y: event.clientY };
    };
    const onPointerMove = ( event) => {
    if (press && event.pointerId === press.pointer) {
    if (Math.hypot(event.clientX - press.x, event.clientY - press.y) < DRAG_THRESHOLD) return;
    const { item, handle, pointer, x, y } = press;
    press = null;
    const rect = item.getBoundingClientRect();
    if (!lift(item, handle, 'pointer', { pointer, grabX: x - rect.left, grabY: y - rect.top, width: rect.width, height: rect.height })) {
    handle.releasePointerCapture?.(pointer);
    return;
    }
    }
    if (!drag || drag.pointer !== event.pointerId) return;
    drag.x = event.clientX;
    drag.y = event.clientY;
    track();
    };
    const onPointerUp = ( event) => {
    if (press && event.pointerId === press.pointer) {
    press.handle.releasePointerCapture?.(press.pointer);
    press = null;
    return;
    }
    if (drag?.pointer === event.pointerId) {
    const up = event.type === 'pointerup';
    end(up, up ? 'up' : 'gone');
    }
    };
    const onPressLost = ( event) => {
    if (press && event.pointerId === press.pointer) press = null;
    };
    const onLostCapture = ( event) => {
    if (drag?.pointer === event.pointerId && !drag.moving && !keepCapture()) end(false);
    };
    const onKeyDown = ( event) => {
    if (drag || event.defaultPrevented || (event.key !== ' ' && event.key !== 'Enter') || event.altKey || event.ctrlKey || event.metaKey) return;
    const handle = event.target?.closest?.(handleSelector);
    if (!handle || !container.contains(handle) || event.target !== handle) return;
    const item = itemOf(handle);
    if (!item || blocked(handle, item)) return;
    if (event.repeat) {
    event.preventDefault();
    return;
    }
    if (lift(item, handle, 'keyboard')) {
    event.preventDefault();
    event.stopPropagation();
    }
    };
    const onFocusIn = ( event) => {
    const handle = event.target?.closest?.(handleSelector);
    if (!handle || !container.contains(handle) || hinted.has(handle)) return;
    regionOf(container);
    const id = hintId(handle);
    if (!id) return;
    const before = handle.getAttribute('aria-describedby');
    hinted.set(handle, before);
    own(handle, 'attrs', 'aria-describedby');
    handle.setAttribute('aria-describedby', before ? `${before} ${id}` : id);
    };
    const dropHint = (handle) => {
    if (!hinted.has(handle)) return;
    const before = hinted.get(handle);
    hinted.delete(handle);
    if (before) handle.setAttribute('aria-describedby', before);
    else handle.removeAttribute('aria-describedby');
    disown(handle, 'attrs', 'aria-describedby');
    };
    const onFocusOut = ( event) => {
    const handle = event.target?.closest?.(handleSelector);
    if (handle) dropHint(handle);
    if (drag?.mode === 'keyboard' && !drag.moving && event.target === drag.handle) end(false);
    };
    const onNative = ( event) => {
    if (drag || press) event.preventDefault();
    };
    container.addEventListener('pointerdown', onPointerDown);
    container.addEventListener('pointermove', onPointerMove);
    container.addEventListener('pointerup', onPointerUp);
    container.addEventListener('pointercancel', onPointerUp);
    container.addEventListener('lostpointercapture', onPressLost);
    container.addEventListener('keydown', onKeyDown);
    container.addEventListener('focusin', onFocusIn);
    container.addEventListener('focusout', onFocusOut);
    container.addEventListener('dragstart', onNative);
    container.addEventListener('selectstart', onNative);
    const controller = {
    get() {
    return drag ? { item: drag.item, mode: drag.mode, from: drag.from, to: drag.current } : null;
    },
    set(value) {
    const item = value?.item;
    if (!item || drag) return false;
    const from = whereIs(item);
    const zone = value.zone ?? from.zone;
    if (!zone || item.contains(zone) || !accepts(item, zone)) return false;
    const others = itemsIn(zone).filter((other) => other !== item);
    const target = { zone, index: clampIndex(value.index ?? from.index, others.length) };
    if (sameTarget(target, from)) return false;
    const context = { item, mode: 'api', from, current: from, zones: zonesFor(item), itemsIn, accepts, layoutBox };
    (options.place ?? defaultPlace)(target, context);
    if (!emit(container, 'drop', { item, from, to: target, mode: 'api' }, { cancelable: true })) {
    (options.place ?? defaultPlace)(from, { ...context, current: target });
    return false;
    }
    say('drop', item, target);
    return true;
    },
    cancel() {
    end(false);
    },
    on(type, fn) {
    const listener = ( event) => {
    if (event.target === container) fn(event.detail);
    };
    container.addEventListener(`ds:${type}`, listener);
    return () => container.removeEventListener(`ds:${type}`, listener);
    },
    destroy() {
    if (instances.get(container) !== controller) return;
    end(false);
    press = null;
    for (const item of [...ending.keys()]) finish(item);
    for (const handle of [...hinted.keys()]) dropHint(handle);
    instances.delete(container);
    container.removeEventListener('pointerdown', onPointerDown);
    container.removeEventListener('pointermove', onPointerMove);
    container.removeEventListener('pointerup', onPointerUp);
    container.removeEventListener('pointercancel', onPointerUp);
    container.removeEventListener('lostpointercapture', onPressLost);
    container.removeEventListener('keydown', onKeyDown);
    container.removeEventListener('focusin', onFocusIn);
    container.removeEventListener('focusout', onFocusOut);
    container.removeEventListener('dragstart', onNative);
    container.removeEventListener('selectstart', onNative);
    },
    };
    instances.set(container, controller);
    if (container.isConnected) regionOf(container);
    return controller;
    }

    return Object.freeze({
      create: create
    });
  });

  add('badge', ['format', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit;
    const enter = DS.motion.enter, leave = DS.motion.leave;
    const DEFAULT_MAX = 99;
    function format(value, max = DEFAULT_MAX) {
    const count = Math.floor(Number(value));
    if (!Number.isFinite(count) || count <= 0) return '';
    return count > max ? `${max}+` : String(count);
    }
    function maximum(text) {
    const parsed = Number.parseInt(text ?? '', 10);
    return Number.isInteger(parsed) && parsed > 0 ? parsed : DEFAULT_MAX;
    }
    function controller(el, start, signal) {
    let value = start.value;
    let max = start.max;
    let leaving = false;
    const listeners = new Set();
    const write = (animate) => {
    const text = format(value, max);
    if (!text) {
    if (!el.textContent || leaving) return;
    if (!animate) {
    el.textContent = '';
    return;
    }
    leaving = true;
    leave(el, 'pop', { then: 'hide' }).then(() => {
    if (!leaving) return;
    leaving = false;
    el.textContent = '';
    el.hidden = false;
    });
    return;
    }
    const appears = !el.textContent || leaving;
    leaving = false;
    if (el.textContent !== text) el.textContent = text;
    if (animate && appears) enter(el, 'pop');
    };
    write(false);
    return {
    get() { return value; },
    set(next) {
    const changed = next !== value;
    value = next;
    write(true);
    if (changed && !signal?.aborted) {
    const detail = { value, text: format(value, max) };
    emit(el, 'change', detail);
    listeners.forEach((fn) => fn(detail));
    }
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    update() {
    value = el.getAttribute('data-ds-badge') ?? value;
    max = maximum(el.getAttribute('data-ds-badge-max'));
    write(true);
    },
    destroy() { listeners.clear(); },
    };
    }
    define('badge', {
    attribute: 'data-ds-badge',
    attributes: ['data-ds-badge-max'],
    init(el, ctx) {
    return controller(el, { value: el.getAttribute('data-ds-badge'), max: maximum(el.getAttribute('data-ds-badge-max')) }, ctx.signal);
    },
    });
    function create(el, options = {}) {
    return controller(el, { value: options.value ?? 0, max: options.max ?? DEFAULT_MAX });
    }

    return Object.freeze({
      format: format,
      create: create
    });
  });

  add('chart', ['ticks', 'timeTicks', 'scale', 'path', 'stack', 'nearest', 'parse', 'layout', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const countUp = DS.motion.countUp, enterAll = DS.motion.enterAll, motionDuration = DS.motion.motionDuration, motionEasing = DS.motion.motionEasing;
    const own = DS.dom.own;
    const iconElement = DS.icons.iconElement, tableGlyph = DS.icons.table;
    const computePlacement = DS.position.computePlacement;
    const announce = DS.a11y.announce;
    const absolute = DS.format.absolute, bytes = DS.format.bytes, number = DS.format.number, percent = DS.format.percent;
    const SVG = 'http://www.w3.org/2000/svg';
    const SECOND = 1000;
    const MINUTE = 60 * SECOND;
    const HOUR = 60 * MINUTE;
    const DAY = 24 * HOUR;
    const WEEK = 7 * DAY;
    const MONTH = 30 * DAY;
    const YEAR = 365 * DAY;
    const KINDS = Object.freeze({
    line: 'cartesian',
    area: 'cartesian',
    column: 'cartesian',
    bar: 'cartesian',
    combo: 'cartesian',
    sparkline: 'spark',
    donut: 'radial',
    pie: 'radial',
    ring: 'radial',
    gauge: 'gauge',
    bargauge: 'rows',
    });
    const SLOTS = 4;
    const MARKERS = [
    (r) => `M${-r} 0a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0Z`,
    (r) => `M${-r * 0.85} ${-r * 0.85}h${r * 1.7}v${r * 1.7}h${-r * 1.7}Z`,
    (r) => `M0 ${-r * 1.1}L${r * 1.05} ${r * 0.8}H${-r * 1.05}Z`,
    (r) => `M0 ${-r * 1.15}L${r * 1.15} 0L0 ${r * 1.15}L${-r * 1.15} 0Z`,
    ];
    const PATTERN_PROPERTIES = ['--ds-chart-pattern-2', '--ds-chart-pattern-3', '--ds-chart-pattern-4'];
    const TONES = ['neutral', 'primary', 'info', 'success', 'warning', 'error'];
    const GAUGE_START = (-2 / 3) * Math.PI;
    const GAUGE_END = (2 / 3) * Math.PI;
    const round = (value) => Math.round(value * 100) / 100;
    const finite = (value) => typeof value === 'number' && Number.isFinite(value);
    const clamp = (value, low, high) => Math.min(high, Math.max(low, value));
    const minOf = (values, start = Infinity) => {
    let out = start;
    for (const value of values) if (value < out) out = value;
    return out;
    };
    const maxOf = (values, start = -Infinity) => {
    let out = start;
    for (const value of values) if (value > out) out = value;
    return out;
    };
    function tickStep(min, max, count) {
    const raw = Math.abs(max - min) / Math.max(1, count);
    if (!(raw > 0) || !Number.isFinite(raw)) return 0;
    const power = 10 ** Math.floor(Math.log10(raw));
    const error = raw / power;
    const factor = error >= Math.sqrt(50) ? 10 : error >= Math.sqrt(10) ? 5 : error >= Math.sqrt(2) ? 2 : 1;
    return factor * power;
    }
    function onStep(index, step) {
    if (step >= 1) return index * step;
    const inverse = Math.round(1 / step);
    return Math.abs(inverse * step - 1) < 1e-9 ? index / inverse : Number((index * step).toPrecision(12));
    }
    function ticks(min, max, count = 5) {
    if (!finite(min) || !finite(max)) return [];
    if (min === max) return [min];
    const reverse = max < min;
    const [low, high] = reverse ? [max, min] : [min, max];
    const step = tickStep(low, high, count);
    if (!step) return [low];
    const out = [];
    for (let index = Math.ceil(low / step - 1e-9); index <= Math.floor(high / step + 1e-9); index += 1) out.push(onStep(index, step) || 0);
    return reverse ? out.reverse() : out;
    }
    function niceDomain(min, max, count) {
    if (min === max) {
    if (min === 0) return [0, 1];
    return min > 0 ? [0, min * 2] : [min * 2, 0];
    }
    let low = min;
    let high = max;
    for (let pass = 0; pass < 2; pass += 1) {
    const step = tickStep(low, high, count);
    if (!step) break;
    low = onStep(Math.floor(low / step + 1e-9), step);
    high = onStep(Math.ceil(high / step - 1e-9), step);
    }
    return [low || 0, high || 0];
    }
    function logTicks(min, max, count) {
    if (!(min > 0) || !(max > 0)) return [];
    const first = Math.floor(Math.log10(min));
    const last = Math.ceil(Math.log10(max));
    const decades = last - first;
    const out = [];
    const every = Math.max(1, Math.ceil(decades / Math.max(1, count)));
    for (let power = first; power <= last; power += 1) {
    const base = 10 ** power;
    const steps = decades < count / 2 ? [1, 2, 5] : (power - first) % every === 0 ? [1] : [];
    for (const factor of steps) {
    const value = Number((factor * base).toPrecision(12));
    if (value >= min * (1 - 1e-9) && value <= max * (1 + 1e-9)) out.push(value);
    }
    }
    return out;
    }
    const TIME_STEPS = [
    ['second', 1, SECOND], ['second', 5, 5 * SECOND], ['second', 15, 15 * SECOND], ['second', 30, 30 * SECOND],
    ['minute', 1, MINUTE], ['minute', 5, 5 * MINUTE], ['minute', 15, 15 * MINUTE], ['minute', 30, 30 * MINUTE],
    ['hour', 1, HOUR], ['hour', 3, 3 * HOUR], ['hour', 6, 6 * HOUR], ['hour', 12, 12 * HOUR],
    ['day', 1, DAY], ['day', 2, 2 * DAY], ['week', 1, WEEK],
    ['month', 1, MONTH], ['month', 3, 3 * MONTH], ['month', 6, 6 * MONTH], ['year', 1, YEAR],
    ];
    function floorTime(date, unit, step) {
    const d = new Date(date);
    switch (unit) {
    case 'second': d.setMilliseconds(0); d.setSeconds(d.getSeconds() - (d.getSeconds() % step)); break;
    case 'minute': d.setSeconds(0, 0); d.setMinutes(d.getMinutes() - (d.getMinutes() % step)); break;
    case 'hour': d.setMinutes(0, 0, 0); d.setHours(d.getHours() - (d.getHours() % step)); break;
    case 'day': d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - ((d.getDate() - 1) % step)); break;
    case 'week': d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - ((d.getDay() + 6) % 7)); break;
    case 'month': d.setHours(0, 0, 0, 0); d.setDate(1); d.setMonth(d.getMonth() - (d.getMonth() % step)); break;
    default: d.setHours(0, 0, 0, 0); d.setMonth(0, 1); d.setFullYear(d.getFullYear() - (d.getFullYear() % step));
    }
    return d;
    }
    function addTime(date, unit, step) {
    const d = new Date(date);
    switch (unit) {
    case 'second': d.setSeconds(d.getSeconds() + step); break;
    case 'minute': d.setMinutes(d.getMinutes() + step); break;
    case 'hour': d.setHours(d.getHours() + step); break;
    case 'day': d.setDate(d.getDate() + step); break;
    case 'week': d.setDate(d.getDate() + 7 * step); break;
    case 'month': d.setMonth(d.getMonth() + step); break;
    default: d.setFullYear(d.getFullYear() + step);
    }
    return d;
    }
    function timeTicks(min, max, count = 5) {
    if (!finite(min) || !finite(max) || max < min) return [];
    if (min === max) return [min];
    const span = max - min;
    const wanted = Math.max(1, count);
    let choice = TIME_STEPS.find(([, , length]) => span / length <= wanted);
    if (!choice) {
    const years = tickStep(new Date(min).getFullYear(), new Date(max).getFullYear(), wanted) || 1;
    choice = ['year', years, years * YEAR];
    }
    const [unit, step] = choice;
    const out = [];
    let at = floorTime(min, unit, step);
    if (at.getTime() < min) at = addTime(at, unit, step);
    for (let guard = 0; at.getTime() <= max && guard < 1000; guard += 1) {
    out.push(at.getTime());
    at = addTime(at, unit, step);
    if (unit !== 'week' && unit !== 'year') at = floorTime(at, unit, step);
    }
    return out;
    }
    function timeTickFormat(value) {
    const d = new Date(value);
    if (d.getSeconds() || d.getMilliseconds()) return { hour: 'numeric', minute: '2-digit', second: '2-digit' };
    if (d.getMinutes() || d.getHours()) return { hour: 'numeric', minute: '2-digit' };
    if (d.getDate() !== 1) return { month: 'short', day: 'numeric' };
    if (d.getMonth()) return { month: 'short' };
    return { year: 'numeric' };
    }
    function timePointFormat(step, span) {
    if (step < DAY) return span > DAY ? { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' } : { hour: 'numeric', minute: '2-digit', ...(step < MINUTE ? { second: '2-digit' } : {}) };
    if (step < 28 * DAY) return { year: 'numeric', month: 'short', day: 'numeric' };
    if (step < YEAR) return { year: 'numeric', month: 'short' };
    return { year: 'numeric' };
    }
    function timesFormat(times) {
    let step = Infinity;
    for (let i = 1; i < times.length; i += 1) if (times[i] > times[i - 1] && times[i] - times[i - 1] < step) step = times[i] - times[i - 1];
    if (!Number.isFinite(step)) step = DAY;
    const span = times.length ? times[times.length - 1] - times[0] : 0;
    return timePointFormat(step, span);
    }
    function scale(type, domain, range, options = {}) {
    const [r0, r1] = range;
    if (type === 'band') {
    const keys = [...domain];
    const n = keys.length;
    const padding = options.padding ?? 0.2;
    const outer = options.outer ?? padding / 2;
    const step = (r1 - r0) / Math.max(1, n - padding + 2 * outer);
    const bandwidth = Math.abs(step * (1 - padding));
    const start = r0 + step * outer;
    const index = new Map(keys.map((key, i) => [String(key), i]));
    const at = (i) => start + i * step + (step * (1 - padding)) / 2;
    return {
    type, domain: keys, range: [r0, r1], bandwidth, step,
    map: (value) => {
    const i = typeof value === 'number' && !index.has(String(value)) ? value : index.get(String(value));
    return i === undefined ? NaN : at(i);
    },
    invert: (px) => (n ? clamp(Math.round((px - start - (step * (1 - padding)) / 2) / step), 0, n - 1) : -1),
    ticks: () => keys,
    };
    }
    const [d0, d1] = domain.map(Number);
    const log = type === 'log';
    const f = log ? (value) => (value > 0 ? Math.log10(value) : NaN) : (value) => value;
    const f0 = f(d0);
    const span = f(d1) - f0;
    const map = (value) => (span ? r0 + ((f(Number(value)) - f0) / span) * (r1 - r0) : (r0 + r1) / 2);
    const invert = (px) => {
    const v = f0 + ((px - r0) / (r1 - r0 || 1)) * span;
    return log ? 10 ** v : v;
    };
    const tickList = (count = 5) => (log ? logTicks(Math.min(d0, d1), Math.max(d0, d1), count) : type === 'time' ? timeTicks(Math.min(d0, d1), Math.max(d0, d1), count) : ticks(d0, d1, count));
    return { type, domain: [d0, d1], range: [r0, r1], bandwidth: 0, step: 0, map, invert, ticks: tickList };
    }
    function monotoneTangents(points) {
    const n = points.length;
    const slopes = [];
    for (let i = 0; i < n - 1; i += 1) {
    const h = points[i + 1].x - points[i].x;
    slopes.push(h ? (points[i + 1].y - points[i].y) / h : 0);
    }
    const tangents = new Array(n).fill(0);
    for (let i = 1; i < n - 1; i += 1) {
    const h0 = points[i].x - points[i - 1].x;
    const h1 = points[i + 1].x - points[i].x;
    const s0 = slopes[i - 1];
    const s1 = slopes[i];
    const p = (s0 * h1 + s1 * h0) / ((h0 + h1) || 1);
    tangents[i] = (Math.sign(s0) + Math.sign(s1)) * Math.min(Math.abs(s0), Math.abs(s1), 0.5 * Math.abs(p)) || 0;
    }
    if (n > 1) {
    const end = (h, slope, inner) => (h ? (3 * slope - inner) / 2 : inner);
    tangents[0] = n > 2 ? end(points[1].x - points[0].x, slopes[0], tangents[1]) : slopes[0];
    tangents[n - 1] = n > 2 ? end(points[n - 1].x - points[n - 2].x, slopes[n - 2], tangents[n - 2]) : slopes[0];
    }
    return tangents;
    }
    function runCommands(run, curve) {
    if (run.length < 2) return [];
    const out = [];
    if (curve === 'step') {
    for (let i = 1; i < run.length; i += 1) out.push(`H${round(run[i].x)}`, `V${round(run[i].y)}`);
    return out;
    }
    if (curve === 'monotone' && run.length > 2) {
    const tangent = monotoneTangents(run);
    for (let i = 1; i < run.length; i += 1) {
    const a = run[i - 1];
    const b = run[i];
    const dx = (b.x - a.x) / 3;
    out.push(`C${round(a.x + dx)} ${round(a.y + dx * tangent[i - 1])} ${round(b.x - dx)} ${round(b.y - dx * tangent[i])} ${round(b.x)} ${round(b.y)}`);
    }
    return out;
    }
    for (let i = 1; i < run.length; i += 1) out.push(`L${round(run[i].x)} ${round(run[i].y)}`);
    return out;
    }
    function runs(points, key = 'y') {
    const out = [];
    let run = [];
    for (const point of points) {
    if (point && finite(point.x) && finite(point[key])) run.push(point);
    else if (run.length) {
    out.push(run);
    run = [];
    }
    }
    if (run.length) out.push(run);
    return out;
    }
    function path(points, options = {}) {
    const curve = options.curve ?? 'linear';
    const parts = [];
    for (const run of runs(points ?? [])) {
    if (!options.area) {
    if (run.length < 2) continue;
    parts.push(`M${round(run[0].x)} ${round(run[0].y)}`, ...runCommands(run, curve));
    continue;
    }
    const top = run.length > 1 ? run : [{ ...run[0], x: run[0].x - 0.5 }, { ...run[0], x: run[0].x + 0.5 }];
    const base = top.map((point) => ({ x: point.x, y: finite(point.y0) ? point.y0 : point.y })).reverse();
    const back = curve === 'step' ? stepBack(base) : runCommands(base, curve);
    parts.push(`M${round(top[0].x)} ${round(top[0].y)}`, ...runCommands(top, curve), `L${round(base[0].x)} ${round(base[0].y)}`, ...back, 'Z');
    }
    return parts.join('');
    }
    function stepBack(base) {
    const out = [];
    for (let i = 1; i < base.length; i += 1) out.push(`V${round(base[i].y)}`, `H${round(base[i].x)}`);
    return out;
    }
    function arcPath(cx, cy, inner, outer, a0, a1) {
    const sweep = a1 - a0;
    if (!(sweep > 1e-6) || !(outer > 0)) return '';
    const full = sweep >= 2 * Math.PI - 1e-6;
    const end = full ? a0 + 2 * Math.PI - 1e-4 : a1;
    const large = end - a0 > Math.PI ? 1 : 0;
    const at = (r, a) => `${round(cx + r * Math.sin(a))} ${round(cy - r * Math.cos(a))}`;
    const outerArc = `M${at(outer, a0)}A${round(outer)} ${round(outer)} 0 ${large} 1 ${at(outer, end)}`;
    if (inner <= 0) return `${outerArc}L${round(cx)} ${round(cy)}Z`;
    return `${outerArc}L${at(inner, end)}A${round(inner)} ${round(inner)} 0 ${large} 0 ${at(inner, a0)}Z`;
    }
    function stack(series, options = {}) {
    const length = Math.max(0, ...series.map((values) => values?.length ?? 0));
    const out = series.map(() => new Array(length).fill(null));
    for (let i = 0; i < length; i += 1) {
    const values = series.map((values) => (finite(values?.[i]) ? values[i] : null));
    const total = values.reduce((sum, value) => sum + Math.abs(value ?? 0), 0);
    const factor = options.offset === 'percent' ? (total ? 1 / total : 0) : 1;
    let up = 0;
    let down = 0;
    values.forEach((value, s) => {
    if (value === null) {
    out[s][i] = null;
    return;
    }
    const v = value * factor;
    if (v >= 0) {
    out[s][i] = [up, up + v];
    up += v;
    } else {
    out[s][i] = [down + v, down];
    down += v;
    }
    });
    }
    return out;
    }
    function nearest(positions, value) {
    const n = positions?.length ?? 0;
    if (!n || !finite(value)) return -1;
    const ascending = positions[n - 1] >= positions[0];
    let low = 0;
    let high = n - 1;
    while (high - low > 1) {
    const mid = (low + high) >> 1;
    if ((positions[mid] < value) === ascending) low = mid;
    else high = mid;
    }
    return Math.abs(positions[low] - value) < Math.abs(positions[high] - value) ? low : high;
    }
    function sliceAt(slices, angle) {
    const a = ((angle % (2 * Math.PI)) + 2 * Math.PI) % (2 * Math.PI);
    return slices.findIndex((slice) => slice.visible && a >= slice.a0 && a < slice.a1);
    }
    function toNumber(value) {
    if (value === null || value === undefined || value === '') return null;
    if (typeof value === 'number') return Number.isFinite(value) ? value : null;
    const text = String(value).trim().replace(/[\s  ]/g, '');
    if (!text || /^[-–—]$/.test(text)) return null;
    const parsed = Number(text.replace(/,/g, ''));
    return Number.isFinite(parsed) ? parsed : null;
    }
    const ISO_DATE = /^\d{4}-\d{2}(-\d{2})?([T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?$/;
    function toTime(value) {
    if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value.getTime();
    if (typeof value === 'string' && ISO_DATE.test(value.trim())) {
    const text = value.trim();
    const parsed = /^\d{4}-\d{2}(-\d{2})?$/.test(text) ? new Date(`${text.length === 7 ? `${text}-01` : text}T00:00`) : new Date(text);
    return Number.isNaN(parsed.getTime()) ? null : parsed.getTime();
    }
    return null;
    }
    function cellValue(cell) {
    const data = cell.getAttribute?.('data-value');
    if (data !== null && data !== undefined) return data;
    const time = cell.querySelector?.('time[datetime]');
    if (time) return time.getAttribute('datetime');
    return (cell.textContent ?? '').trim();
    }
    function parse(table) {
    const head = table?.tHead?.rows?.[0] ?? table?.rows?.[0];
    const headCells = head ? [...head.cells] : [];
    const bodyRows = table?.tBodies?.length ? [...table.tBodies].flatMap((body) => [...body.rows]) : [...(table?.rows ?? [])].slice(1);
    const series = headCells.slice(1).map((cell) => {
    const entry = { name: (cell.textContent ?? '').trim(), values: [] };
    for (const [attribute, key] of [['data-ds-chart-mark', 'mark'], ['data-ds-chart-axis', 'axis'], ['data-ds-chart-tone', 'tone']]) {
    const value = cell.getAttribute?.(attribute);
    if (value) entry[key] = value;
    }
    return entry;
    });
    const labels = [];
    for (const row of bodyRows) {
    const cells = [...row.cells];
    if (!cells.length) continue;
    labels.push(cellValue(cells[0]));
    series.forEach((entry, index) => entry.values.push(cells[index + 1] ? toNumber(cellValue(cells[index + 1])) : null));
    }
    return { labels, series, x: { label: (headCells[0]?.textContent ?? '').trim() } };
    }
    const toneOf = (value) => (TONES.includes(value) ? value : null);
    function normalize(input, previous) {
    const spec = { ...(input ?? {}) };
    const type = KINDS[spec.type] ? spec.type : 'line';
    const kind = KINDS[type];
    const rawLabels = Array.isArray(spec.labels) ? spec.labels : [];
    const rawSeries = Array.isArray(spec.series) ? spec.series : finite(spec.value) ? [{ name: spec.name ?? '', values: [spec.value] }] : [];
    const length = Math.max(rawLabels.length, ...rawSeries.map((entry) => (Array.isArray(entry?.values) ? entry.values.length : 0)), 0);
    const labels = Array.from({ length }, (_, i) => rawLabels[i] ?? i + 1);
    const times = labels.map(toTime);
    const xOption = spec.x ?? {};
    const hasColumns = type === 'column' || type === 'bar' || type === 'combo' || (type === 'sparkline' && spec.mark === 'bar');
    let xType = xOption.type;
    if (!['time', 'linear', 'band'].includes(xType)) {
    if (hasColumns) xType = 'band';
    else if (labels.length && times.every((value) => value !== null)) xType = 'time';
    else if (labels.length && labels.every((value) => typeof value === 'number')) xType = 'linear';
    else xType = 'band';
    }
    const dates = labels.length > 0 && times.every((value) => value !== null);
    const xs = xType === 'time' ? times : xType === 'linear' ? labels.map(Number) : labels.map((_, i) => i);
    const keys = labels.map((label, i) => (dates ? String(times[i]) : String(label)));
    const hidden = new Set(previous ? previous.series.filter((entry) => !entry.visible).map((entry) => entry.name) : []);
    const series = rawSeries.map((entry, index) => {
    const values = Array.from({ length }, (_, i) => toNumber(entry?.values?.[i]));
    let mark = ['line', 'area', 'column'].includes(entry?.mark) ? entry.mark : null;
    if (!mark) {
    if (type === 'area') mark = 'area';
    else if (type === 'column' || type === 'bar') mark = 'column';
    else if (type === 'combo') mark = index === rawSeries.length - 1 && rawSeries.length > 1 ? 'line' : 'column';
    else if (type === 'sparkline') mark = spec.mark === 'bar' ? 'column' : spec.mark === 'area' ? 'area' : 'line';
    else mark = 'line';
    }
    const axis = entry?.axis === 'end' || (type === 'combo' && spec.y2 && mark === 'line' && entry?.axis !== 'start') ? 'end' : 'start';
    const name = String(entry?.name ?? '');
    return {
    name, values, mark, axis,
    tone: toneOf(entry?.tone),
    slot: index < SLOTS ? index : -1,
    visible: entry?.visible === false ? false : !hidden.has(name),
    };
    });
    const stackMode = spec.stack === 'percent' || spec.stack === true || spec.stack === 'normal' ? (spec.stack === 'percent' ? 'percent' : 'normal') : null;
    return {
    type, kind,
    horizontal: type === 'bar',
    labels, keys, xs, dates, xType,
    timeFormat: dates ? timesFormat(times) : null,
    series,
    stack: stackMode,
    curve: ['linear', 'monotone', 'step'].includes(spec.curve) ? spec.curve : 'linear',
    markers: spec.markers ?? 'auto',
    patterns: spec.patterns === true,
    x: { label: xOption.label ?? '', format: xOption.format ?? null },
    y: { type: spec.y?.type === 'log' ? 'log' : 'linear', min: toNumber(spec.y?.min), max: toNumber(spec.y?.max), label: spec.y?.label ?? '', zero: spec.y?.zero ?? kind !== 'spark', format: spec.y?.format ?? spec.format ?? null },
    y2: spec.y2 ? { type: spec.y2.type === 'log' ? 'log' : 'linear', min: toNumber(spec.y2.min), max: toNumber(spec.y2.max), label: spec.y2.label ?? '', zero: spec.y2.zero !== false, format: spec.y2.format ?? null } : null,
    format: spec.format ?? null,
    thresholds: (Array.isArray(spec.thresholds) ? spec.thresholds : []).map((entry) => ({
    value: toNumber(entry?.value), from: toNumber(entry?.from), to: toNumber(entry?.to),
    label: String(entry?.label ?? ''), axis: entry?.axis === 'end' ? 'end' : 'start', tone: toneOf(entry?.tone),
    })).filter((entry) => entry.value !== null || (entry.from !== null && entry.to !== null)),
    annotations: (Array.isArray(spec.annotations) ? spec.annotations : []).map((entry) => ({
    at: entry?.at ?? null, from: entry?.from ?? null, to: entry?.to ?? null, label: String(entry?.label ?? ''), tone: toneOf(entry?.tone),
    })),
    min: toNumber(spec.min) ?? 0,
    max: toNumber(spec.max),
    zones: (Array.isArray(spec.zones) ? spec.zones : []).map((zone) => ({ to: toNumber(zone?.to), tone: toneOf(zone?.tone), label: String(zone?.label ?? '') })),
    legend: spec.legend ?? (kind === 'cartesian' || kind === 'radial'),
    table: spec.table !== false && kind !== 'gauge' && kind !== 'rows' && kind !== 'spark',
    tooltip: spec.tooltip !== false && kind !== 'gauge' && kind !== 'rows' && !(kind === 'spark' && spec.tooltip !== true),
    label: spec.label ?? null,
    title: spec.title ?? '',
    };
    }
    function formatValue(value, format, compact = false) {
    if (value === null || value === undefined || !Number.isFinite(value)) return '';
    if (format === 'bytes') return bytes(value);
    if (format === 'percent') return percent(value, { maximumFractionDigits: 1 });
    if (format && typeof format === 'object') return number(value, format);
    return compact ? number(value, { notation: 'compact', maximumFractionDigits: 1 }) : number(value, { maximumFractionDigits: 2 });
    }
    const handFormats = new WeakMap();
    function formatX(spec, index) {
    const label = spec.labels[index];
    if (spec.dates) {
    let format = spec.x.format ?? spec.timeFormat ?? handFormats.get(spec);
    if (!format) {
    format = timesFormat(spec.keys.map(Number));
    handFormats.set(spec, format);
    }
    return absolute(Number(spec.keys[index]), format);
    }
    if (typeof label === 'number') return formatValue(label, spec.x.format);
    return String(label ?? '');
    }
    const estimate = (text, size = 12) => String(text).length * size * 0.56;
    function axisDomain(values, options, needsZero) {
    let min = Infinity;
    let max = -Infinity;
    for (const value of values) {
    if (!finite(value)) continue;
    if (options.type === 'log' && value <= 0) continue;
    min = Math.min(min, value);
    max = Math.max(max, value);
    }
    if (min === Infinity) [min, max] = options.type === 'log' ? [1, 10] : [0, 1];
    if (options.type !== 'log' && (needsZero || options.zero)) {
    min = Math.min(min, 0);
    max = Math.max(max, 0);
    }
    if (options.min !== null) min = options.min;
    if (options.max !== null) max = options.max;
    return [min, max];
    }
    function layout(spec, width, height, metrics = {}) {
    const s = spec?.kind ? spec : normalize(spec);
    const m = {
    measure: metrics.measure ?? ((text) => estimate(text)),
    line: metrics.line ?? 16,
    gap: metrics.gap ?? 8,
    bar: metrics.bar ?? 24,
    seam: metrics.seam ?? 2,
    marker: metrics.marker ?? 8,
    };
    const box = { width: Math.max(0, width), height: Math.max(0, height) };
    if (s.kind === 'radial') return radialLayout(s, box, m);
    if (s.kind === 'gauge') return gaugeLayout(s, box, m);
    if (s.kind === 'rows') return rowsLayout(s);
    return cartesianLayout(s, box, m);
    }
    function seriesRanges(s) {
    const ranges = s.series.map((entry) => entry.values.map((value) => (value === null ? null : entry.mark === 'line' && !s.stack ? [value, value] : [0, value])));
    if (!s.stack) return ranges;
    const groups = new Map();
    s.series.forEach((entry, index) => {
    if (!entry.visible) return;
    const group = `${entry.axis}:${entry.mark === 'column' ? 'column' : 'area'}`;
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(index);
    });
    for (const members of groups.values()) {
    const stacked = stack(members.map((index) => s.series[index].values), { offset: s.stack === 'percent' ? 'percent' : 'normal' });
    members.forEach((index, i) => { ranges[index] = stacked[i]; });
    }
    return ranges;
    }
    function valueAxis(options, domain, length, from, to, percentScale) {
    const count = Math.max(2, Math.round(length / 56));
    let [low, high] = domain;
    if (options.type === 'log') {
    low = 10 ** Math.floor(Math.log10(low));
    high = 10 ** Math.ceil(Math.log10(high));
    if (low === high) high = low * 10;
    } else if (!(options.min !== null && options.max !== null)) {
    const [niceLow, niceHigh] = niceDomain(low, high, count);
    if (options.min === null) low = niceLow;
    if (options.max === null) high = niceHigh;
    }
    if (percentScale && options.max === null) high = Math.min(high, 1);
    const sc = scale(options.type === 'log' ? 'log' : 'linear', [low, high], [from, to]);
    const values = sc.ticks(count).filter((value) => value >= Math.min(low, high) - 1e-9 && value <= Math.max(low, high) + 1e-9);
    const compact = Math.max(Math.abs(low), Math.abs(high)) >= 10000;
    const format = percentScale ? 'percent' : options.format;
    return { scale: sc, ticks: values.map((value) => ({ value, label: formatValue(value, format, compact && !format) })) };
    }
    function cartesianLayout(s, box, m) {
    const spark = s.kind === 'spark';
    const visible = s.series.filter((entry) => entry.visible);
    const ranges = seriesRanges(s);
    const n = s.labels.length;
    const percentScale = s.stack === 'percent';
    const axes = ['start', 'end'].map((axis) => {
    const members = s.series.map((entry, index) => ({ entry, index })).filter(({ entry }) => entry.visible && entry.axis === axis);
    if (!members.length && (axis === 'end' || visible.length)) return null;
    const options = axis === 'end' ? s.y2 ?? s.y : s.y;
    const values = [];
    for (const { index } of members) for (const range of ranges[index]) if (range) values.push(range[0], range[1]);
    for (const entry of s.thresholds) if (entry.axis === axis) values.push(entry.value, entry.from, entry.to);
    const needsZero = members.some(({ entry }) => entry.mark !== 'line') || percentScale;
    return { axis, options, members, domain: axisDomain(values, options, needsZero), needsZero };
    });
    const [startAxis, endAxis] = axes;
    const line = m.line;
    const hasTitles = !spark && Boolean(s.y.label || (endAxis && s.y2?.label));
    const crossLabels = !spark && (s.horizontal ? s.thresholds.some((entry) => entry.label && entry.value !== null) : s.annotations.some((entry) => entry.label && entry.at !== null));
    const top = spark ? m.marker / 2 + 1 : line / 2 + (hasTitles || crossLabels ? line + m.gap / 2 : 0);
    const bottom = spark ? m.marker / 2 + 1 : line + m.gap;
    const labelText = s.labels.map((_, i) => (s.xType === 'band' ? formatX(s, i) : ''));
    let start = spark ? m.marker / 2 + 1 : 0;
    let end = spark ? m.marker / 2 + 1 : m.gap;
    const plotTop = top;
    const plotBottom = box.height - bottom;
    const plotHeight = Math.max(1, plotBottom - plotTop);
    let valueStart = null;
    let valueEnd = null;
    const widestLabel = s.xType === 'band' && !spark ? maxOf(labelText.map((text) => m.measure(text)), 0) : 0;
    if (s.horizontal) {
    start = spark ? start : Math.min(box.width * 0.4, widestLabel) + m.gap;
    } else if (!spark) {
    valueStart = startAxis ? valueAxis(startAxis.options, startAxis.domain, plotHeight, plotBottom, plotTop, percentScale && startAxis.needsZero) : null;
    valueEnd = endAxis ? valueAxis(endAxis.options, endAxis.domain, plotHeight, plotBottom, plotTop, false) : null;
    start = valueStart ? Math.max(...valueStart.ticks.map((tick) => m.measure(tick.label)), 0) + m.gap : m.gap;
    if (valueEnd) end = Math.max(...valueEnd.ticks.map((tick) => m.measure(tick.label)), 0) + m.gap;
    }
    const plot = { x: start, y: plotTop, width: Math.max(1, box.width - start - end), height: plotHeight };
    const plotRight = plot.x + plot.width;
    if (s.horizontal) {
    valueStart = startAxis ? valueAxis(startAxis.options, startAxis.domain, plot.width, plot.x, plotRight, percentScale) : null;
    if (spark && valueStart) valueStart = { scale: scale('linear', valueStart.scale.domain, [plot.x, plotRight]), ticks: [] };
    }
    if (spark && !s.horizontal) {
    const domain = startAxis ? startAxis.domain : [0, 1];
    valueStart = { scale: scale(s.y.type === 'log' ? 'log' : 'linear', domain[0] === domain[1] ? [domain[0] - 1, domain[1] + 1] : domain, [plotBottom, plotTop]), ticks: [] };
    }
    const along = s.horizontal ? [plot.y, plotBottom] : [plot.x, plotRight];
    const length = Math.abs(along[1] - along[0]);
    let xScale;
    if (s.xType === 'band') {
    const lineOnly = s.series.every((entry) => entry.mark !== 'column');
    xScale = scale('band', s.keys, along, lineOnly ? { padding: 0, outer: 0.5 } : { padding: spark ? 0.25 : 0.3 });
    } else {
    const defined = s.xs.filter(finite);
    const lo = defined.length ? minOf(defined) : 0;
    const hi = defined.length ? maxOf(defined) : 1;
    xScale = scale(s.xType === 'time' ? 'time' : 'linear', lo === hi ? [lo - 1, hi + 1] : [lo, hi], along);
    }
    const positions = s.labels.map((_, i) => (s.xType === 'band' ? xScale.map(i) : xScale.map(s.xs[i])));
    let xTicks = [];
    if (!spark) {
    if (s.xType === 'band') {
    const room = s.horizontal ? line + 4 : Math.max(widestLabel, 1) + m.gap * 2;
    const every = Math.max(1, Math.ceil((n * room) / Math.max(1, length)));
    xTicks = labelText.map((label, i) => ({ value: i, pos: positions[i], label })).filter((_, i) => (n - 1 - i) % every === 0);
    } else {
    const count = Math.max(2, Math.floor(length / (s.xType === 'time' ? 96 : 72)));
    const values = xScale.ticks(count);
    const compact = Math.max(...values.map(Math.abs), 0) >= 10000;
    xTicks = values.map((value) => ({
    value,
    pos: xScale.map(value),
    label: s.xType === 'time' ? absolute(value, timeTickFormat(value)) : formatValue(value, s.x.format, compact),
    }));
    xTicks = xTicks.filter((tick, i, all) => {
    const half = m.measure(tick.label) / 2;
    const prev = all[i - 1];
    return !prev || tick.pos - half > prev.pos + m.measure(prev.label) / 2 + m.gap;
    });
    }
    }
    const valueScale = (entry) => (entry.axis === 'end' && valueEnd ? valueEnd.scale : valueStart?.scale);
    const columns = s.series.map((entry, index) => ({ entry, index })).filter(({ entry }) => entry.mark === 'column');
    const groups = s.stack ? [...new Set(columns.map(({ entry }) => entry.axis))].length : columns.length;
    const band = xScale.bandwidth || 0;
    const thickness = groups ? Math.max(1, Math.min(m.bar, (band - (groups - 1) * m.seam) / groups)) : 0;
    const groupWidth = groups ? groups * thickness + (groups - 1) * m.seam : 0;
    const series = s.series.map((entry, index) => {
    const sc = valueScale(entry);
    const range = ranges[index];
    const base = sc ? sc.map(sc.type === 'log' ? sc.domain[0] : clamp(0, Math.min(...sc.domain), Math.max(...sc.domain))) : 0;
    const points = s.labels.map((_, i) => {
    const r = range[i];
    if (!r || !sc || !entry.visible) return { key: s.keys[i], x: positions[i], y: null, y0: null, value: entry.values[i] };
    const y = sc.map(r[1]);
    const y0 = entry.mark === 'line' && !s.stack ? base : sc.map(r[0]);
    return s.horizontal
    ? { key: s.keys[i], x: y, y: positions[i], y0, value: entry.values[i] }
    : { key: s.keys[i], x: positions[i], y, y0, value: entry.values[i] };
    });
    let bars = [];
    if (entry.mark === 'column' && sc) {
    const slot = s.stack ? [...new Set(columns.map(({ entry: e }) => e.axis))].indexOf(entry.axis) : columns.findIndex(({ index: i }) => i === index);
    const offset = -groupWidth / 2 + slot * (thickness + m.seam);
    const [p0, p1] = s.horizontal ? [plot.x, plotRight] : [plotBottom, plot.y];
    const share = (value) => clamp((sc.map(value) - p0) / (p1 - p0 || 1), 0, 1);
    bars = s.labels.map((_, i) => {
    const r = range[i];
    const at = positions[i] + offset;
    const valueNow = entry.values[i];
    const zeroShare = share(sc.type === 'log' ? sc.domain[0] : clamp(0, Math.min(...sc.domain), Math.max(...sc.domain)));
    const lo = !r || !entry.visible ? zeroShare : share(r[0]);
    const hi = !r || !entry.visible ? zeroShare : share(r[1]);
    const down = finite(valueNow) && valueNow < 0;
    return {
    key: s.keys[i], index: i, value: valueNow,
    x: s.horizontal ? plot.x : at, y: s.horizontal ? at : plot.y,
    width: s.horizontal ? plot.width : thickness, height: s.horizontal ? thickness : plot.height,
    from: Math.min(lo, hi), share: Math.max(lo, hi), down,
    inner: false, onStack: false,
    };
    });
    if (s.stack) {
    bars.forEach((bar) => { bar.onStack = range[bar.index] ? (bar.down ? range[bar.index][1] !== 0 : range[bar.index][0] !== 0) : false; });
    }
    }
    return { ...entry, index, points, bars };
    });
    if (s.stack) {
    for (let i = 0; i < n; i += 1) {
    for (const sign of [1, -1]) {
    const members = series.filter((entry) => entry.mark === 'column' && entry.visible && entry.bars[i] && finite(entry.values[i]) && Math.sign(entry.values[i] || 0) === sign && entry.values[i] !== 0);
    members.forEach((entry, k) => { entry.bars[i].inner = k < members.length - 1; });
    }
    }
    }
    for (const entry of series) entry.drawn = entry.points;
    if (n > 2 * Math.max(1, length)) {
    const along = s.horizontal ? 'y' : 'x';
    const paths = series.filter((entry) => entry.mark !== 'column');
    const shown = paths.filter((entry) => entry.visible);
    for (const entry of paths) if (!entry.visible) entry.drawn = [];
    const pick = (entry, indices) => { entry.drawn = [...indices].sort((a, b) => a - b).map((i) => entry.points[i]); };
    if (s.stack) {
    const keep = new Set();
    for (const entry of shown) reduceIndices(entry.points, along, keep);
    for (const entry of shown) pick(entry, keep);
    } else for (const entry of shown) pick(entry, reduceIndices(entry.points, along, new Set()));
    }
    const rules = s.thresholds.map((entry) => {
    const sc = entry.axis === 'end' && valueEnd ? valueEnd.scale : valueStart?.scale;
    if (!sc) return null;
    if (entry.value !== null) {
    const at = sc.map(entry.value);
    return { kind: 'line', axis: 'value', at, label: entry.label, tone: entry.tone };
    }
    const a = sc.map(entry.from);
    const b = sc.map(entry.to);
    return { kind: 'band', axis: 'value', from: Math.min(a, b), to: Math.max(a, b), label: entry.label, tone: entry.tone };
    }).filter(Boolean);
    const xOf = (value) => {
    if (s.xType === 'band') {
    const key = s.dates ? String(toTime(value) ?? value) : String(value);
    const i = s.keys.indexOf(key);
    return i < 0 ? NaN : positions[i];
    }
    const v = s.xType === 'time' ? toTime(value) ?? Number(value) : Number(value);
    return xScale.map(v);
    };
    for (const entry of s.annotations) {
    if (entry.at !== null) {
    const at = xOf(entry.at);
    if (finite(at)) rules.push({ kind: 'line', axis: 'x', at, label: entry.label, tone: entry.tone });
    } else if (entry.from !== null && entry.to !== null) {
    let a = xOf(entry.from);
    let b = xOf(entry.to);
    if (s.xType === 'band') {
    a -= xScale.step / 2;
    b += xScale.step / 2;
    }
    if (finite(a) && finite(b)) rules.push({ kind: 'band', axis: 'x', from: Math.min(a, b), to: Math.max(a, b), label: entry.label, tone: entry.tone });
    }
    }
    const markers = s.markers === true || (s.markers === 'auto' && !spark && n > 0 && length / n >= 24);
    return {
    kind: s.kind, box, plot, horizontal: s.horizontal, spark,
    x: xScale, y: valueStart?.scale ?? null, y2: valueEnd?.scale ?? null,
    xTicks, yTicks: valueStart?.ticks ?? [], y2Ticks: valueEnd?.ticks ?? [],
    titles: hasTitles ? { start: s.y.label, end: endAxis ? s.y2?.label ?? '' : '' } : null,
    baseline: valueStart ? valueStart.scale.map(valueStart.scale.type === 'log' ? valueStart.scale.domain[0] : clamp(0, Math.min(...valueStart.scale.domain), Math.max(...valueStart.scale.domain))) : null,
    positions, series, rules, markers, band: xScale.step || 0,
    };
    }
    function reduceIndices(points, along, into) {
    const across = along === 'x' ? 'y' : 'x';
    let bucket = NaN;
    let first = -1;
    let low = -1;
    let high = -1;
    let last = -1;
    let gap = false;
    const flush = () => {
    if (first >= 0) into.add(first).add(low).add(high).add(last);
    first = -1;
    };
    points.forEach((point, i) => {
    if (!point || !finite(point[along]) || !finite(point[across])) {
    flush();
    if (!gap) into.add(i);
    gap = true;
    bucket = NaN;
    return;
    }
    gap = false;
    const column = Math.floor(point[along]);
    if (column !== bucket) {
    flush();
    bucket = column;
    first = i;
    low = i;
    high = i;
    }
    if (point[across] < points[low][across]) low = i;
    if (point[across] > points[high][across]) high = i;
    last = i;
    });
    flush();
    return into;
    }
    function radialLayout(s, box, m) {
    const size = Math.max(0, Math.min(box.width, box.height));
    const cx = box.width / 2;
    const cy = box.height / 2;
    const outer = size / 2 - 1;
    if (s.type === 'ring') {
    const rings = s.series.slice(0, SLOTS);
    const thickness = Math.max(4, Math.min(16, (outer * 0.55) / Math.max(1, rings.length) - m.seam));
    const items = rings.map((entry, i) => {
    const value = entry.values.find(finite) ?? 0;
    const max = s.max ?? 100;
    const share = clamp((value - s.min) / ((max - s.min) || 1), 0, 1);
    const r1 = outer - i * (thickness + m.seam * 2);
    return { key: entry.name, name: entry.name, value, max, share, slot: entry.slot, tone: entry.tone, visible: entry.visible, inner: r1 - thickness, outer: r1, a0: 0, a1: entry.visible ? share * 2 * Math.PI : 0, index: i };
    });
    return { kind: 'radial', type: 'ring', box, cx, cy, outer, inner: outer - rings.length * (thickness + m.seam * 2), slices: items, total: null, positions: items.map((_, i) => i) };
    }
    const source = s.series[0];
    let slices = (source ? source.values : []).map((value, i) => ({ key: s.keys[i], name: formatX(s, i), value: Math.max(0, value ?? 0), slot: i < SLOTS ? i : -1, index: i, tone: null }));
    if (slices.length > SLOTS + 1) {
    const rest = slices.slice(SLOTS);
    slices = [...slices.slice(0, SLOTS), { key: '\u0000other', name: t('chartOther'), value: rest.reduce((sum, slice) => sum + slice.value, 0), slot: -1, index: SLOTS, tone: null, other: rest.map((slice) => slice.index) }];
    }
    const hiddenKeys = new Set((s.hiddenSlices ?? []));
    slices.forEach((slice) => { slice.visible = !hiddenKeys.has(slice.key); });
    const total = slices.reduce((sum, slice) => sum + (slice.visible ? slice.value : 0), 0);
    const inner = s.type === 'pie' ? 0 : outer * 0.62;
    const pad = slices.filter((slice) => slice.visible && slice.value > 0).length > 1 ? m.seam / Math.max(1, outer) : 0;
    let angle = 0;
    for (const slice of slices) {
    const sweep = total && slice.visible ? (slice.value / total) * 2 * Math.PI : 0;
    slice.a0 = angle;
    slice.a1 = angle + sweep;
    slice.pad = sweep > pad * 2 ? pad / 2 : 0;
    slice.share = total && slice.visible ? slice.value / total : 0;
    slice.outer = outer;
    slice.inner = inner;
    angle += sweep;
    }
    return { kind: 'radial', type: s.type, box, cx, cy, outer, inner, slices, total, positions: slices.map((_, i) => i) };
    }
    function gaugeLayout(s, box, m) {
    const max = s.max ?? 100;
    const min = s.min ?? 0;
    const value = s.series[0]?.values.find(finite) ?? null;
    const reach = s.zones.length ? Math.max(2, m.seam * 2) * 2 : 0;
    const room = Math.max(0, box.height - m.line - 6);
    const whole = Math.max(0, Math.min(box.width / 2, room / 1.5) - 1);
    const outer = Math.max(0, whole - reach);
    const thickness = Math.max(6, Math.min(20, outer * 0.16));
    const cx = box.width / 2;
    const cy = whole + 1 + Math.max(0, (room - whole * 1.5) / 2);
    const angleOf = (v) => GAUGE_START + clamp((v - min) / ((max - min) || 1), 0, 1) * (GAUGE_END - GAUGE_START);
    let from = min;
    const zones = s.zones.map((zone, i) => {
    const to = zone.to ?? max;
    const out = { from, to, tone: zone.tone, label: zone.label, a0: angleOf(from), a1: angleOf(to), index: i };
    from = to;
    return out;
    });
    const zone = value === null ? null : zones.find((entry) => value <= entry.to) ?? zones[zones.length - 1] ?? null;
    return {
    kind: 'gauge', box, cx, cy, outer, inner: outer - thickness, thickness, min, max, value,
    a0: GAUGE_START, a1: GAUGE_END, angle: value === null ? GAUGE_START : angleOf(value),
    zones, zone, band: Math.max(2, m.seam * 2),
    ends: [GAUGE_START, GAUGE_END].map((a, i) => ({ x: cx + (outer - thickness / 2) * Math.sin(a), y: cy - (outer + reach) * Math.cos(a) + 4, label: formatValue(i ? max : min, s.format) })),
    };
    }
    function rowsLayout(s) {
    const source = s.series[0];
    const values = source ? source.values : [];
    const max = s.max ?? maxOf(values.filter(finite), 0);
    const rows = values.map((value, i) => {
    const zone = finite(value) ? s.zones.find((entry) => entry.to === null || value <= entry.to) ?? null : null;
    return {
    key: s.keys[i], label: formatX(s, i), value, text: formatValue(value, s.format),
    share: finite(value) ? clamp((value - s.min) / ((max - s.min) || 1), 0, 1) : 0,
    tone: zone?.tone ?? source?.tone ?? null, status: zone?.label ?? '',
    };
    });
    return { kind: 'rows', rows, max };
    }
    let uid = 0;
    const svg = (doc, name, attrs = {}, cls = '') => {
    const el = doc.createElementNS(SVG, name);
    if (cls) el.setAttribute('class', cls);
    for (const [key, value] of Object.entries(attrs)) if (value !== null && value !== undefined) el.setAttribute(key, String(value));
    return el;
    };
    const html = (doc, name, cls = '', text = null) => {
    const el = doc.createElement(name);
    if (cls) el.className = cls;
    if (text !== null) el.textContent = text;
    return el;
    };
    function attrs(el, values) {
    for (const [key, value] of Object.entries(values)) {
    const text = value === null || value === undefined ? null : String(value);
    if (text === null) el.removeAttribute(key);
    else if (el.getAttribute(key) !== text) el.setAttribute(key, text);
    }
    return el;
    }
    function seriesClass(base, entry) {
    const slot = entry.slot < 0 ? 'ds-chart__series--other' : entry.slot > 0 ? `ds-chart__series--s${entry.slot + 1}` : '';
    return [...new Set([base, 'ds-chart__series', slot, entry.tone ? `ds-tone-${entry.tone}` : ''])].filter(Boolean).join(' ');
    }
    function keyed(parent, items, keyOf, make) {
    const existing = new Map();
    for (const child of [...parent.children]) existing.set(child.getAttribute('data-key'), child);
    const seen = new Set();
    let before = parent.firstChild;
    const out = items.map((item, i) => {
    const key = String(keyOf(item, i));
    seen.add(key);
    let el = existing.get(key);
    const fresh = !el;
    if (!el) {
    el = make(item, i);
    el.setAttribute('data-key', key);
    }
    if (el !== before) parent.insertBefore(el, before);
    before = el.nextSibling;
    return { el, item, fresh };
    });
    for (const [key, child] of existing) if (!seen.has(key)) child.remove();
    return out;
    }
    const canvases = new WeakMap();
    function measurer(el) {
    const doc = el.ownerDocument;
    const view = doc?.defaultView;
    const style = view?.getComputedStyle?.(el);
    const size = parseFloat(style?.fontSize ?? '') || 12;
    let canvas = canvases.get(doc);
    if (!canvas && doc?.createElement) {
    const context = doc.createElement('canvas').getContext?.('2d') ?? null;
    canvas = { context };
    canvases.set(doc, canvas);
    }
    const context = canvas?.context;
    if (context && style) {
    context.font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
    return (text) => context.measureText(String(text)).width * 1.04;
    }
    return (text) => estimate(text, size);
    }
    function lengthOf(style, name, fallback) {
    const value = parseFloat(style?.getPropertyValue(name) ?? '');
    return Number.isFinite(value) ? value : fallback;
    }
    function blendPoints(from, to, p) {
    return to.map((point) => {
    const start = from.get(point.key);
    if (!start || !finite(point.y) || !finite(start.y)) return point;
    return {
    ...point,
    x: start.x + (point.x - start.x) * p,
    y: start.y + (point.y - start.y) * p,
    y0: finite(point.y0) && finite(start.y0) ? start.y0 + (point.y0 - start.y0) * p : point.y0,
    };
    });
    }
    function alongAxis(shown, horizontal) {
    const along = horizontal ? 'y' : 'x';
    return [...(shown?.values() ?? [])].filter((entry) => finite(entry.x) && finite(entry.y)).sort((a, b) => a[along] - b[along]);
    }
    function entryPoint(sorted, point, horizontal, baseline) {
    const along = horizontal ? 'y' : 'x';
    const across = horizontal ? 'x' : 'y';
    if (!sorted.length) {
    const flat = finite(baseline) ? baseline : point[across];
    return { ...point, [across]: flat, y0: finite(point.y0) ? flat : point.y0 };
    }
    const at = point[along];
    let value;
    let base;
    if (at <= sorted[0][along]) [value, base] = [sorted[0][across], sorted[0].y0];
    else if (at >= sorted[sorted.length - 1][along]) [value, base] = [sorted[sorted.length - 1][across], sorted[sorted.length - 1].y0];
    else {
    let lo = 0;
    let hi = sorted.length - 1;
    while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (sorted[mid][along] >= at) hi = mid;
    else lo = mid + 1;
    }
    const a = sorted[lo - 1];
    const b = sorted[lo];
    const k = (at - a[along]) / ((b[along] - a[along]) || 1);
    value = a[across] + (b[across] - a[across]) * k;
    base = finite(a.y0) && finite(b.y0) ? a.y0 + (b.y0 - a.y0) * k : point.y0;
    }
    return { ...point, [across]: value, y0: finite(point.y0) ? base : point.y0 };
    }
    function specOf(el) {
    const raw = (el.getAttribute('data-ds-chart') ?? '').trim();
    let spec = {};
    if (raw.startsWith('{')) {
    try {
    spec = JSON.parse(raw);
    } catch {
    spec = {};
    }
    } else if (raw) {
    spec = { type: raw };
    }
    const config = el.getAttribute('data-ds-config');
    if (config) {
    try {
    spec = { ...JSON.parse(config), ...spec };
    } catch {
    }
    }
    const table = sourceTable(el);
    if (table && !Array.isArray(spec.series)) spec = { ...parse(table), ...spec, x: { ...parse(table).x, ...(spec.x ?? {}) } };
    return spec;
    }
    function sourceTable(el) {
    return [...el.children].find((child) => child.tagName === 'TABLE') ?? [...el.children].find((child) => !child.hasAttribute('data-ds-keep') && child.querySelector?.(':scope > table'))?.querySelector(':scope > table') ?? null;
    }
    const KEYS = new Set(['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown', 'Enter', 'Escape']);
    function controller(el, initial, signal) {
    const doc = el.ownerDocument;
    const view = doc.defaultView;
    const id = `ds-chart-${(uid += 1)}`;
    const listeners = new Map();
    let spec = normalize(initial);
    let input = { ...(initial ?? {}) };
    let current = null;
    let shown = new Map();
    let arcsShown = new Map();
    let morph = null;
    let size = { width: 0, height: 0 };
    let cursor = { index: -1, series: -1, active: false };
    let marks = { series: -1, slice: -1 };
    let viewMode = 'chart';
    let first = true;
    let destroyed = false;
    let hiddenSlices = new Set();
    if (!el.classList.contains('ds-chart')) {
    own(el, 'classes', 'ds-chart');
    el.classList.add('ds-chart');
    }
    own(el, 'attrs', 'data-state');
    el.setAttribute('data-state', 'chart');
    const typeClasses = () => [`ds-chart--${spec.type}`, spec.stack ? 'ds-chart--stacked' : '', spec.patterns ? 'ds-chart--patterns' : '', isEmpty() ? 'ds-chart--empty' : ''].filter(Boolean);
    const isEmpty = () => !spec.series.some((entry) => entry.values.some(finite));
    let applied = [];
    const keep = (node) => {
    node.setAttribute('data-ds-keep', '');
    return node;
    };
    const partClass = (entry, extra = '', position = -1) => [
    seriesClass('ds-chart__series', entry),
    extra,
    marks.series >= 0 && entry.index !== marks.series ? 'ds-chart__series--dim' : '',
    marks.slice >= 0 && position === marks.slice ? 'ds-chart__slice--active' : '',
    ].filter(Boolean).join(' ');
    const tools = keep(html(doc, 'div', 'ds-chart__tools'));
    const toggle = html(doc, 'button', 'ds-btn ds-btn--quiet ds-btn--icon ds-sm ds-chart__view');
    toggle.type = 'button';
    toggle.setAttribute('aria-pressed', 'false');
    toggle.append(iconElement(tableGlyph, 20));
    tools.append(toggle);
    const legend = keep(html(doc, 'ul', 'ds-chart__legend'));
    const frame = keep(html(doc, 'div', 'ds-chart__frame'));
    const hint = html(doc, 'span', 'ds-chart__hint');
    hint.id = `${id}-hint`;
    const root = svg(doc, 'svg', { 'aria-hidden': 'true', focusable: 'false' }, 'ds-chart__svg');
    const defs = svg(doc, 'defs');
    const clip = svg(doc, 'clipPath', { id: `${id}-clip` });
    const clipRect = svg(doc, 'rect');
    clip.append(clipRect);
    defs.append(clip);
    for (let slot = 1; slot < SLOTS; slot += 1) {
    const pattern = svg(doc, 'pattern', { id: `${id}-p${slot + 1}`, patternUnits: 'userSpaceOnUse', width: 6, height: 6, patternTransform: `rotate(${[0, 45, 135, 0][slot]})` }, `ds-chart__pattern ds-chart__series ds-chart__series--s${slot + 1}`);
    pattern.append(svg(doc, 'rect', { width: 6, height: 6 }, 'ds-chart__pattern-ground'), svg(doc, 'rect', { width: 2.5, height: 6 }, 'ds-chart__pattern-ink'));
    defs.append(pattern);
    root.style.setProperty(PATTERN_PROPERTIES[slot - 1], `url(#${id}-p${slot + 1})`);
    }
    const layer = (cls, clipped = false) => {
    const g = svg(doc, 'g', clipped ? { 'clip-path': `url(#${id}-clip)` } : {}, cls);
    root.append(g);
    return g;
    };
    root.append(defs);
    const gGrid = layer('ds-chart__grid');
    const gBands = layer('ds-chart__bands', true);
    const gHover = layer('ds-chart__hover', true);
    const hoverRect = svg(doc, 'rect', {}, 'ds-chart__band');
    gHover.append(hoverRect);
    const gBars = layer('ds-chart__bars');
    const gAreas = layer('ds-chart__areas', true);
    const gLines = layer('ds-chart__lines', true);
    const gRules = layer('ds-chart__rules');
    const gAxes = layer('ds-chart__axes');
    const cursorLine = svg(doc, 'line', {}, 'ds-chart__cursor');
    root.append(cursorLine);
    const gMarkers = layer('ds-chart__markers');
    const gDots = layer('ds-chart__dots');
    const centre = html(doc, 'div', 'ds-chart__centre');
    const centreValue = html(doc, 'span', 'ds-chart__figure');
    const centreLabel = html(doc, 'span', 'ds-chart__caption');
    centre.append(centreValue, centreLabel);
    const probe = html(doc, 'span', 'ds-chart__probe');
    probe.setAttribute('aria-hidden', 'true');
    const emptyNote = html(doc, 'span', 'ds-chart__empty');
    const tip = html(doc, 'div', 'ds-chart__tip');
    tip.setAttribute('aria-hidden', 'true');
    const rowsBox = html(doc, 'ul', 'ds-chart__rows');
    frame.append(root, centre, tip, hint, probe, emptyNote);
    const tableBox = keep(html(doc, 'div', 'ds-chart__table'));
    const mount = () => {
    const caption = el.querySelector(':scope > figcaption');
    const anchor = caption ? caption.nextSibling : el.firstChild;
    el.insertBefore(tools, anchor);
    el.insertBefore(legend, tools.nextSibling);
    el.insertBefore(frame, legend.nextSibling);
    if (!sourceTable(el)) el.append(tableBox);
    else tableBox.remove();
    };
    const titleText = () => {
    const caption = el.querySelector(':scope > figcaption');
    return spec.title || el.getAttribute('aria-label') || (caption?.textContent ?? '').trim();
    };
    const emitOut = (type, detail) => {
    if (destroyed) return;
    emit(el, type, detail);
    for (const fn of listeners.get(type) ?? []) fn(detail);
    };
    const metrics = () => {
    const style = view?.getComputedStyle?.(probe);
    const axisStyle = view?.getComputedStyle?.(gAxes);
    return {
    measure: measurer(gAxes),
    line: lengthOf(axisStyle, 'line-height', 16) || 16,
    gap: lengthOf(style, 'padding-block-start', 8),
    bar: lengthOf(style, 'inline-size', 24) || 24,
    seam: lengthOf(style, 'padding-inline-start', 2),
    marker: lengthOf(style, 'block-size', 8) || 8,
    };
    };
    const name = () => titleText() || spec.series.map((entry) => entry.name).filter(Boolean).join(', ');
    const noRole = { role: null, tabindex: null, 'aria-roledescription': null, 'aria-label': null, 'aria-describedby': null, 'aria-valuemin': null, 'aria-valuemax': null, 'aria-valuenow': null, 'aria-valuetext': null };
    const interactive = (label) => ({ role: 'application', tabindex: '0', 'aria-roledescription': t('chartRole'), 'aria-label': label || null, 'aria-describedby': hint.id });
    const applyRoles = () => {
    const title = name();
    if (spec.kind === 'gauge') {
    attrs(frame, { ...noRole, role: 'meter', 'aria-label': title || null,
    'aria-valuemin': spec.min, 'aria-valuemax': spec.max ?? 100, 'aria-valuenow': current?.value ?? null,
    'aria-valuetext': current?.value === null || current?.value === undefined ? null : [formatValue(current.value, spec.format), current.zone?.label].filter(Boolean).join(', ') });
    return;
    }
    if (spec.kind === 'rows') {
    attrs(frame, noRole);
    return;
    }
    if (spec.kind === 'spark') {
    attrs(frame, spec.tooltip ? { ...noRole, ...interactive(summary()) } : { ...noRole, role: 'img', 'aria-label': summary() || null });
    return;
    }
    if (isEmpty()) {
    attrs(frame, { ...noRole, role: 'img', 'aria-label': [title, t('chartEmpty')].filter(Boolean).join(': ') });
    return;
    }
    attrs(frame, { ...noRole, ...interactive(title) });
    };
    const summary = () => {
    const entry = spec.series.find((item) => item.visible) ?? spec.series[0];
    const values = (entry?.values ?? []).filter(finite);
    if (!values.length) return name();
    const text = t('chartSummary', {
    name: titleText() || entry.name,
    first: formatValue(values[0], spec.format),
    last: formatValue(values[values.length - 1], spec.format),
    min: formatValue(minOf(values), spec.format),
    max: formatValue(maxOf(values), spec.format),
    });
    return text;
    };
    const legendItems = () => {
    if (spec.kind === 'radial' && spec.type !== 'ring') return (current?.slices ?? []).map((slice) => ({ key: slice.key, name: slice.name, slot: slice.slot, tone: null, mark: 'column', visible: slice.visible, value: slice.value, share: slice.share }));
    return spec.series.map((entry, index) => ({ key: String(index), name: entry.name, slot: entry.slot, tone: entry.tone, mark: spec.type === 'ring' ? 'column' : entry.mark, visible: entry.visible, index, value: spec.type === 'ring' ? current?.slices?.[index]?.value : undefined }));
    };
    const swatch = (item) => {
    const box = svg(doc, 'svg', { viewBox: '0 0 20 12', 'aria-hidden': 'true', focusable: 'false' }, seriesClass('ds-chart__swatch', item));
    if (item.mark === 'line') {
    box.append(svg(doc, 'path', { d: 'M1 6H19' }, 'ds-chart__swatch-line'));
    box.append(svg(doc, 'path', { d: MARKERS[Math.max(0, item.slot) % MARKERS.length](3), transform: 'translate(10 6)' }, 'ds-chart__swatch-marker'));
    } else {
    box.append(svg(doc, 'rect', { x: 4, y: 0, width: 12, height: 12 }, 'ds-chart__swatch-fill'));
    }
    return box;
    };
    const renderLegend = () => {
    const items = spec.legend && (spec.kind === 'radial' || spec.series.length > 1) ? legendItems() : [];
    legend.hidden = !items.length;
    keyed(legend, items, (item) => `${item.key}:${item.mark}:${item.slot}:${item.tone}`, (item) => {
    const li = html(doc, 'li');
    const button = html(doc, 'button', 'ds-chart__key');
    button.type = 'button';
    button.setAttribute('data-series', item.key);
    button.append(swatch(item), html(doc, 'span', 'ds-chart__key-name'));
    if (spec.kind === 'radial') button.append(html(doc, 'span', 'ds-chart__key-value'));
    li.append(button);
    return li;
    }).forEach(({ el: li, item }) => {
    const button = li.firstElementChild;
    button.setAttribute('aria-pressed', String(item.visible));
    const nameEl = button.querySelector('.ds-chart__key-name');
    if (nameEl.textContent !== item.name) nameEl.textContent = item.name;
    const valueEl = button.querySelector('.ds-chart__key-value');
    if (valueEl) {
    const text = !item.visible ? '' : spec.type === 'ring' ? formatValue(item.value, spec.format) : formatValue(item.share, 'percent');
    if (valueEl.textContent !== text) valueEl.textContent = text;
    }
    });
    };
    let tableFor = null;
    const renderTable = () => {
    const title = titleText();
    if (tableFor && tableFor.spec === spec && tableFor.title === title) return;
    tableFor = { spec, title };
    if (sourceTable(el) || !spec.table) {
    tableBox.replaceChildren();
    return;
    }
    const table = html(doc, 'table', 'ds-table');
    const caption = html(doc, 'caption', 'ds-chart__table-caption', title);
    const head = html(doc, 'thead');
    const headRow = html(doc, 'tr');
    const corner = html(doc, 'th', '', spec.x.label);
    corner.scope = 'col';
    headRow.append(corner);
    const columns = spec.kind === 'radial' && spec.type !== 'ring' ? [spec.series[0]].filter(Boolean) : spec.series;
    for (const entry of columns) {
    const th = html(doc, 'th', 'ds-num', entry.name);
    th.scope = 'col';
    headRow.append(th);
    }
    head.append(headRow);
    const body = html(doc, 'tbody');
    if (spec.type === 'ring') {
    body.append(...spec.series.map((entry) => {
    const tr = html(doc, 'tr');
    const th = html(doc, 'th', '', entry.name);
    th.scope = 'row';
    tr.append(th, html(doc, 'td', 'ds-num', formatValue(entry.values.find(finite) ?? null, spec.format)));
    return tr;
    }));
    headRow.replaceChildren(corner, Object.assign(html(doc, 'th', 'ds-num', ''), { scope: 'col' }));
    } else {
    spec.labels.forEach((_, i) => {
    const tr = html(doc, 'tr');
    const th = html(doc, 'th', '', formatX(spec, i));
    th.scope = 'row';
    tr.append(th, ...columns.map((entry) => html(doc, 'td', 'ds-num', formatValue(entry.values[i], spec.format))));
    body.append(tr);
    });
    }
    table.append(caption, head, body);
    tableBox.replaceChildren(table);
    };
    const textEl = (parent, attributes, text, cls) => {
    const node = svg(doc, 'text', attributes, cls);
    node.textContent = text;
    parent.append(node);
    return node;
    };
    const drawAxes = (lay) => {
    gGrid.replaceChildren();
    gAxes.replaceChildren();
    gRules.replaceChildren();
    gBands.replaceChildren();
    if (lay.kind !== 'cartesian' && lay.kind !== 'spark') return;
    const { plot } = lay;
    const right = plot.x + plot.width;
    const bottom = plot.y + plot.height;
    if (!lay.spark) {
    for (const tick of lay.yTicks) {
    const at = round(lay.horizontal ? lay.y.map(tick.value) : lay.y.map(tick.value));
    if (lay.horizontal) gGrid.append(svg(doc, 'line', { x1: at, x2: at, y1: plot.y, y2: bottom }, 'ds-chart__gridline'));
    else gGrid.append(svg(doc, 'line', { x1: plot.x, x2: right, y1: at, y2: at }, 'ds-chart__gridline'));
    }
    if (finite(lay.baseline)) {
    const at = round(lay.baseline);
    gGrid.append(lay.horizontal ? svg(doc, 'line', { x1: at, x2: at, y1: plot.y, y2: bottom }, 'ds-chart__baseline') : svg(doc, 'line', { x1: plot.x, x2: right, y1: at, y2: at }, 'ds-chart__baseline'));
    }
    const m = current?.metrics ?? { gap: 8, line: 16 };
    if (lay.horizontal) {
    for (const tick of lay.yTicks) textEl(gAxes, { x: round(lay.y.map(tick.value)), y: round(bottom + m.gap), 'text-anchor': 'middle', 'dominant-baseline': 'hanging' }, tick.label, 'ds-chart__tick');
    for (const tick of lay.xTicks) textEl(gAxes, { x: round(plot.x - m.gap), y: round(tick.pos), 'text-anchor': 'end', 'dominant-baseline': 'central' }, tick.label, 'ds-chart__tick ds-chart__tick--category');
    } else {
    for (const tick of lay.yTicks) textEl(gAxes, { x: round(plot.x - m.gap), y: round(lay.y.map(tick.value)), 'text-anchor': 'end', 'dominant-baseline': 'central' }, tick.label, 'ds-chart__tick');
    for (const tick of lay.y2Ticks) textEl(gAxes, { x: round(right + m.gap), y: round(lay.y2.map(tick.value)), 'text-anchor': 'start', 'dominant-baseline': 'central' }, tick.label, 'ds-chart__tick');
    const width = lay.box.width;
    for (const tick of lay.xTicks) {
    const half = m.measure ? m.measure(tick.label) / 2 : 0;
    const x = clamp(tick.pos, half, width - half);
    textEl(gAxes, { x: round(x), y: round(bottom + m.gap), 'text-anchor': 'middle', 'dominant-baseline': 'hanging' }, tick.label, 'ds-chart__tick');
    }
    }
    if (lay.titles) {
    if (lay.titles.start) textEl(gAxes, { x: 0, y: 0, 'dominant-baseline': 'hanging' }, lay.titles.start, 'ds-chart__axis-title');
    if (lay.titles.end) textEl(gAxes, { x: lay.box.width, y: 0, 'text-anchor': 'end', 'dominant-baseline': 'hanging' }, lay.titles.end, 'ds-chart__axis-title');
    }
    }
    for (const rule of lay.rules) {
    const cls = rule.tone ? ` ds-tone-${rule.tone}` : '';
    const valueAxisHorizontal = rule.axis === 'value' ? lay.horizontal : !lay.horizontal;
    if (rule.kind === 'band') {
    const rect = valueAxisHorizontal
    ? { x: rule.from, y: plot.y, width: rule.to - rule.from, height: plot.height }
    : { x: plot.x, y: rule.from, width: plot.width, height: rule.to - rule.from };
    gBands.append(svg(doc, 'rect', Object.fromEntries(Object.entries(rect).map(([k, v]) => [k, round(v)])), `ds-chart__zone${cls}`));
    if (rule.label && !lay.spark) {
    const wide = (current?.metrics?.measure ?? estimate)(rule.label);
    const at = valueAxisHorizontal ? { x: round(Math.max(plot.x, Math.min(rule.from + 4, right - 4 - wide))), y: round(plot.y + 4), 'dominant-baseline': 'hanging' } : { x: round(right - 4), y: round(rule.from + 4), 'text-anchor': 'end', 'dominant-baseline': 'hanging' };
    textEl(gRules, at, rule.label, 'ds-chart__rule-label');
    }
    continue;
    }
    const line = valueAxisHorizontal ? { x1: rule.at, x2: rule.at, y1: plot.y, y2: bottom } : { x1: plot.x, x2: right, y1: rule.at, y2: rule.at };
    gRules.append(svg(doc, 'line', Object.fromEntries(Object.entries(line).map(([k, v]) => [k, round(v)])), `ds-chart__rule${cls}`));
    if (rule.label && !lay.spark) {
    let at;
    if (valueAxisHorizontal) {
    const half = (current?.metrics?.measure ?? estimate)(rule.label) / 2;
    at = { x: round(clamp(rule.at, half, lay.box.width - half)), y: round(plot.y - 4), 'text-anchor': 'middle' };
    } else at = { x: round(right - 4), y: round(rule.at - 4), 'text-anchor': 'end' };
    textEl(gRules, at, rule.label, 'ds-chart__rule-label');
    }
    }
    };
    const drawBars = (lay, animate) => {
    const entries = (lay.series ?? []).filter((entry) => entry.mark === 'column');
    const fresh = [];
    keyed(gBars, entries, (entry) => entry.index, (entry) => svg(doc, 'g')).forEach(({ el: g, item: entry }) => {
    attrs(g, { class: partClass(entry), 'data-series': entry.index });
    keyed(g, entry.bars, (bar) => bar.key, () => svg(doc, 'rect', {}, 'ds-chart__bar')).forEach(({ el: rect, item: bar, fresh: isNew }) => {
    attrs(rect, {
    x: round(bar.x), y: round(bar.y), width: round(Math.max(0, bar.width)), height: round(Math.max(0, bar.height)),
    class: ['ds-chart__bar', bar.down ? 'ds-chart__bar--down' : '', bar.inner ? 'ds-chart__bar--inner' : '', bar.onStack ? 'ds-chart__bar--on' : ''].filter(Boolean).join(' '),
    });
    rect.style.setProperty('--ds-share', String(round(bar.share * 1000) / 1000));
    rect.style.setProperty('--ds-chart-from', String(round(bar.from * 1000) / 1000));
    if (isNew) fresh.push(rect);
    });
    });
    if (animate && fresh.length) enterAll(fresh, 'fill', { stagger: false });
    };
    const drawMarkers = (lay, points) => {
    if (!lay.markers) {
    gMarkers.replaceChildren();
    return;
    }
    const groups = (lay.series ?? []).filter((entry) => entry.mark !== 'column' && entry.visible);
    keyed(gMarkers, groups, (entry) => entry.index, () => svg(doc, 'g')).forEach(({ el: g, item: entry }) => {
    attrs(g, { class: seriesClass('ds-chart__series', entry) });
    const pts = (points.get(entry.index) ?? []).filter((point) => finite(point.x) && finite(point.y));
    const shape = MARKERS[Math.max(0, entry.slot) % MARKERS.length](current.metrics.marker / 2);
    keyed(g, pts, (point) => point.key, () => svg(doc, 'path', { d: shape }, 'ds-chart__marker')).forEach(({ el: marker, item: point }) => {
    attrs(marker, { d: shape, transform: `translate(${round(point.x)} ${round(point.y)})` });
    });
    });
    };
    const lonePoints = (points) => points.filter((point, i) => finite(point.y) && !finite(points[i - 1]?.y) && !finite(points[i + 1]?.y));
    const drawPaths = (lay, frameOf) => {
    const lines = (lay.series ?? []).filter((entry) => entry.mark !== 'column');
    const curve = spec.curve;
    const pointsNow = new Map();
    keyed(gAreas, lines.filter((entry) => entry.mark === 'area'), (entry) => entry.index, () => svg(doc, 'path', {}, 'ds-chart__area')).forEach(({ el: node, item: entry }) => {
    const pts = frameOf(entry);
    attrs(node, { class: partClass(entry, 'ds-chart__area'), 'data-series': entry.index, d: entry.visible ? path(pts, { curve, area: true }) : '' });
    });
    keyed(gLines, lines, (entry) => entry.index, () => svg(doc, 'g')).forEach(({ el: g, item: entry }) => {
    const pts = frameOf(entry);
    pointsNow.set(entry.index, pts);
    attrs(g, { class: partClass(entry), 'data-series': entry.index, 'data-hidden': entry.visible ? null : '' });
    let line = g.querySelector('.ds-chart__line');
    if (!line) {
    line = svg(doc, 'path', {}, 'ds-chart__line');
    g.append(line);
    }
    attrs(line, { d: entry.visible ? path(pts, { curve }) : '' });
    const lone = entry.visible ? lonePoints(pts) : [];
    const shape = MARKERS[Math.max(0, entry.slot) % MARKERS.length](current.metrics.marker / 2);
    const holder = g.querySelector('.ds-chart__lone') ?? g.appendChild(svg(doc, 'g', {}, 'ds-chart__lone'));
    keyed(holder, lone, (point) => point.key, () => svg(doc, 'path', {}, 'ds-chart__marker')).forEach(({ el: marker, item: point }) => {
    attrs(marker, { d: shape, transform: `translate(${round(point.x)} ${round(point.y)})` });
    });
    });
    drawMarkers(lay, pointsNow);
    return pointsNow;
    };
    const drawRadial = (lay, frameOf) => {
    gBars.replaceChildren();
    const slices = lay.slices ?? [];
    const tracks = lay.type === 'ring' ? slices : [];
    keyed(gAreas, tracks, (item) => item.key, () => svg(doc, 'path', {}, 'ds-chart__track')).forEach(({ el: node, item }) => {
    attrs(node, { d: arcPath(lay.cx, lay.cy, item.inner, item.outer, 0, 2 * Math.PI) });
    });
    keyed(gLines, slices, (item) => item.key, () => svg(doc, 'path')).forEach(({ el: node, item }, position) => {
    const angles = frameOf(item);
    attrs(node, {
    class: partClass(item, 'ds-chart__slice', position),
    'data-series': item.index,
    d: arcPath(lay.cx, lay.cy, item.inner, item.outer, angles.a0 + (item.pad ?? 0), angles.a1 - (item.pad ?? 0)),
    });
    });
    };
    const drawGauge = (lay, angleNow) => {
    gBars.replaceChildren();
    gLines.replaceChildren();
    const track = svg(doc, 'path', { d: arcPath(lay.cx, lay.cy, lay.inner, lay.outer, lay.a0, lay.a1) }, 'ds-chart__track');
    gLines.append(track);
    if (lay.zones.length) {
    const r1 = lay.outer + lay.band * 2;
    for (const zone of lay.zones) {
    gLines.append(svg(doc, 'path', { d: arcPath(lay.cx, lay.cy, r1 - lay.band, r1, zone.a0 + 0.01, zone.a1 - 0.01) }, `ds-chart__zone-arc${zone.tone ? ` ds-tone-${zone.tone}` : ''}`));
    }
    }
    const tone = lay.zone?.tone;
    gLines.append(svg(doc, 'path', { d: arcPath(lay.cx, lay.cy, lay.inner, lay.outer, lay.a0, angleNow) }, `ds-chart__slice ds-chart__value-arc${tone ? ` ds-tone-${tone}` : ''}`));
    gAxes.replaceChildren();
    for (const end of lay.ends) textEl(gAxes, { x: round(end.x), y: round(end.y), 'text-anchor': 'middle', 'dominant-baseline': 'hanging' }, end.label, 'ds-chart__tick');
    };
    const drawRows = (lay, animate) => {
    keyed(rowsBox, lay.rows, (row) => row.key, () => {
    const li = html(doc, 'li', 'ds-chart__row');
    const head = html(doc, 'span', 'ds-chart__row-head');
    head.append(html(doc, 'span', 'ds-chart__row-label'), html(doc, 'span', 'ds-chart__row-status'), html(doc, 'strong', 'ds-chart__row-value ds-num'));
    const bar = html(doc, 'span', 'ds-chart__row-bar');
    bar.setAttribute('role', 'meter');
    li.append(head, bar);
    return li;
    }).forEach(({ el: li, item: row, fresh }) => {
    li.className = `ds-chart__row${row.tone ? ` ds-tone-${row.tone}` : ''}`;
    const label = li.querySelector('.ds-chart__row-label');
    if (label.textContent !== row.label) label.textContent = row.label;
    const status = li.querySelector('.ds-chart__row-status');
    if (status.textContent !== row.status) status.textContent = row.status;
    const bar = li.querySelector('.ds-chart__row-bar');
    attrs(bar, {
    'aria-label': row.label, 'aria-valuemin': spec.min, 'aria-valuemax': lay.max,
    'aria-valuenow': finite(row.value) ? row.value : null,
    'aria-valuetext': [row.text, row.status].filter(Boolean).join(', ') || null,
    });
    bar.style.setProperty('--ds-share', String(round(row.share * 1000) / 1000));
    const value = li.querySelector('.ds-chart__row-value');
    if (animate && !fresh && finite(row.value)) countUp(value, row.value, { format: (v) => formatValue(v, spec.format) });
    else value.textContent = row.text;
    if (animate && fresh) enterAll([bar], 'fill', { stagger: false });
    });
    };
    const stopMorph = () => {
    if (!morph) return;
    view?.cancelAnimationFrame?.(morph.frame);
    morph.clock?.cancel();
    morph = null;
    };
    const startClock = () => {
    const duration = motionDuration(root, '--ds-dur-glide');
    if (!(duration > 0) || typeof root.animate !== 'function' || !root.isConnected || typeof view?.requestAnimationFrame !== 'function') return null;
    try {
    const clock = root.animate([], { duration, easing: motionEasing(root, '--ds-ease-value'), id: 'ds-chart-morph' });
    clock.id = 'ds-chart-morph';
    return clock;
    } catch {
    return null;
    }
    };
    const progressOf = (clock) => {
    if (!clock || clock.playState === 'idle' || clock.playState === 'finished') return 1;
    const value = clock.effect?.getComputedTiming?.().progress;
    return value === null || value === undefined ? 0 : clamp(value, 0, 1);
    };
    const paintCartesian = (lay, animate) => {
    drawAxes(lay);
    drawBars(lay, animate && !first ? true : animate);
    const lines = lay.series.filter((entry) => entry.mark !== 'column');
    const targets = new Map(lines.map((entry) => [entry.index, entry.drawn ?? entry.points]));
    const from = new Map();
    for (const entry of lines) {
    const before = shown.get(entry.index);
    let sorted = null;
    const map = new Map();
    for (const point of targets.get(entry.index)) {
    if (!finite(point.y) && !finite(point.x)) continue;
    const known = before?.get(point.key);
    if (known) {
    map.set(point.key, known);
    continue;
    }
    sorted ??= alongAxis(before, lay.horizontal);
    map.set(point.key, entryPoint(sorted, point, lay.horizontal, lay.baseline));
    }
    from.set(entry.index, map);
    }
    const draw = (p) => {
    const now = drawPaths(lay, (entry) => (p >= 1 ? targets.get(entry.index) : blendPoints(from.get(entry.index) ?? new Map(), targets.get(entry.index), p)));
    shown = new Map([...now].map(([index, pts]) => [index, new Map(pts.filter((point) => finite(point.x) && finite(point.y)).map((point) => [point.key, point]))]));
    };
    runMorph(animate && lines.length > 0, draw);
    };
    const paintRadial = (lay, animate) => {
    drawAxes(lay);
    const from = new Map();
    for (const item of lay.slices) {
    const before = arcsShown.get(item.key);
    const at = before ?? (first ? { a0: 0, a1: 0 } : { a0: item.a0, a1: item.a0 });
    from.set(item.key, at);
    }
    const draw = (p) => {
    const now = new Map();
    drawRadial(lay, (item) => {
    const a = from.get(item.key);
    const frame = p >= 1 ? { a0: item.a0, a1: item.a1 } : { a0: a.a0 + (item.a0 - a.a0) * p, a1: a.a1 + (item.a1 - a.a1) * p };
    now.set(item.key, frame);
    return frame;
    });
    arcsShown = now;
    };
    runMorph(animate, draw);
    if (lay.type === 'donut' || lay.type === 'ring') {
    const value = lay.type === 'ring' ? lay.slices[0]?.value ?? null : lay.total;
    const format = lay.type === 'ring' ? spec.format : spec.format;
    centre.hidden = false;
    const label = spec.label ?? (lay.type === 'ring' ? lay.slices[0]?.name ?? '' : t('chartTotal'));
    if (centreLabel.textContent !== label) centreLabel.textContent = label;
    if (animate && finite(value)) countUp(centreValue, value, { format: (v) => formatValue(v, format) });
    else centreValue.textContent = formatValue(value, format);
    } else centre.hidden = true;
    };
    const paintGauge = (lay, animate) => {
    const before = arcsShown.get('value')?.a1 ?? lay.a0;
    const draw = (p) => {
    const angle = p >= 1 ? lay.angle : before + (lay.angle - before) * p;
    drawGauge(lay, angle);
    arcsShown = new Map([['value', { a0: lay.a0, a1: angle }]]);
    };
    runMorph(animate, draw);
    centre.hidden = false;
    const label = [lay.zone?.label, spec.label].filter(Boolean).join(' · ');
    if (centreLabel.textContent !== label) centreLabel.textContent = label;
    if (animate && finite(lay.value)) countUp(centreValue, lay.value, { format: (v) => formatValue(v, spec.format) });
    else centreValue.textContent = formatValue(lay.value, spec.format);
    };
    const runMorph = (animate, draw) => {
    stopMorph();
    const clock = animate ? startClock() : null;
    if (!clock) {
    draw(1);
    return;
    }
    const run = { clock, frame: 0 };
    morph = run;
    const tick = () => {
    if (morph !== run) return;
    const p = progressOf(clock);
    draw(p);
    if (p >= 1 || clock.playState === 'finished' || clock.playState === 'idle') {
    draw(1);
    if (morph === run) morph = null;
    return;
    }
    run.frame = view.requestAnimationFrame(tick);
    };
    clock.finished?.then(() => {
    if (morph !== run) return;
    view.cancelAnimationFrame?.(run.frame);
    draw(1);
    morph = null;
    }, () => {});
    draw(progressOf(clock));
    run.frame = view.requestAnimationFrame(tick);
    };
    const freeze = () => { stopMorph(); };
    const render = (animate) => {
    if (destroyed) return;
    const wanted = typeClasses();
    if (wanted.join(' ') !== applied.join(' ')) {
    for (const name of applied) if (!wanted.includes(name)) el.classList.remove(name);
    for (const name of wanted) {
    own(el, 'classes', name);
    el.classList.add(name);
    }
    applied = wanted;
    }
    if (spec.kind === 'rows') {
    if (!rowsBox.isConnected) frame.append(rowsBox);
    root.remove();
    const lay = layout(spec, 0, 0);
    current = { ...lay, metrics: metrics() };
    drawRows(lay, animate);
    renderLegend();
    renderTable();
    applyRoles();
    tools.hidden = true;
    return;
    }
    rowsBox.remove();
    if (!root.isConnected) frame.prepend(root);
    const rect = frame.getBoundingClientRect?.() ?? { width: 0, height: 0 };
    size = { width: Math.round(rect.width), height: Math.round(rect.height) };
    const m = metrics();
    freeze();
    const lay = layout({ ...spec, hiddenSlices: [...hiddenSlices] }, size.width, size.height, m);
    current = { ...lay, metrics: m };
    attrs(root, { viewBox: `0 0 ${size.width} ${size.height}`, width: size.width, height: size.height });
    if (lay.plot) attrs(clipRect, { x: round(lay.plot.x - m.marker), y: round(lay.plot.y - m.marker), width: round(lay.plot.width + 2 * m.marker), height: round(lay.plot.height + 2 * m.marker) });
    else attrs(clipRect, { x: 0, y: 0, width: size.width, height: size.height });
    const empty = isEmpty();
    emptyNote.textContent = empty ? t('chartEmpty') : '';
    tools.hidden = !spec.table || empty;
    toggle.setAttribute('aria-label', t('chartTableView'));
    toggle.setAttribute('data-tip', t('chartTableView'));
    hint.textContent = t(spec.kind === 'radial' ? 'chartKeysSlices' : spec.horizontal ? 'chartKeysBars' : 'chartKeys');
    if (lay.kind === 'radial' || lay.kind === 'gauge') {
    centre.style.setProperty('--ds-chart-cx', `${round(lay.cx)}px`);
    centre.style.setProperty('--ds-chart-cy', `${round(lay.cy)}px`);
    centre.style.setProperty('--ds-chart-hole', `${round(Math.max(0, lay.inner) * 2)}px`);
    }
    if (lay.kind === 'radial') {
    gAreas.replaceChildren();
    gMarkers.replaceChildren();
    paintRadial(lay, animate);
    } else if (lay.kind === 'gauge') {
    gAreas.replaceChildren();
    gMarkers.replaceChildren();
    paintGauge(lay, animate);
    } else {
    centre.hidden = true;
    paintCartesian(lay, animate);
    }
    renderLegend();
    if (empty) legend.hidden = true;
    renderTable();
    applyRoles();
    if (cursor.active) showCursor(false);
    first = false;
    };
    const pointCount = () => (current?.kind === 'radial' ? current.slices.length : spec.labels.length);
    const visibleSeries = () => spec.series.map((entry, index) => ({ entry, index })).filter(({ entry }) => entry.visible);
    const pointDetail = () => {
    const i = cursor.index;
    if (current?.kind === 'radial') {
    const slice = current.slices[i];
    return slice ? { index: i, series: i, key: slice.key, label: slice.name } : null;
    }
    return i < 0 ? null : { index: i, series: cursor.series, key: spec.keys[i], label: formatX(spec, i) };
    };
    const readout = () => {
    const i = cursor.index;
    if (current?.kind === 'radial') {
    const slice = current.slices[i];
    if (!slice) return null;
    const value = current.type === 'ring' ? formatValue(slice.value, spec.format) : `${formatValue(slice.value, spec.format)} (${formatValue(slice.share, 'percent')})`;
    return { title: '', rows: [{ entry: { slot: slice.slot, tone: slice.tone, mark: 'column' }, name: slice.name, value, hidden: !slice.visible }] };
    }
    const rows = visibleSeries()
    .filter(({ index }) => cursor.series < 0 || index === cursor.series)
    .map(({ entry }) => ({ entry, name: entry.name, value: entry.values[i] === null ? '–' : formatValue(entry.values[i], spec.stack === 'percent' ? spec.format : spec.format) }));
    if (spec.stack && cursor.series < 0 && rows.length > 1) {
    const total = visibleSeries().reduce((sum, { entry }) => sum + (entry.values[i] ?? 0), 0);
    rows.push({ entry: null, name: t('chartTotal'), value: formatValue(total, spec.format) });
    }
    return { title: formatX(spec, i), rows };
    };
    const fillTip = (data) => {
    const children = [];
    if (data.title) children.push(html(doc, 'span', 'ds-chart__tip-title', data.title));
    for (const row of data.rows) {
    const line = html(doc, 'span', 'ds-chart__tip-row');
    if (row.entry) {
    const key = svg(doc, 'svg', { viewBox: '0 0 16 4', 'aria-hidden': 'true', focusable: 'false' }, seriesClass('ds-chart__tip-key', row.entry));
    key.append(svg(doc, 'path', { d: 'M1 2H15' }, row.entry.mark === 'column' ? 'ds-chart__tip-key-bar' : 'ds-chart__swatch-line'));
    line.append(key);
    } else line.append(html(doc, 'span', 'ds-chart__tip-gap'));
    line.append(html(doc, 'strong', 'ds-chart__tip-value', row.value), html(doc, 'span', 'ds-chart__tip-name', row.name));
    children.push(line);
    }
    tip.replaceChildren(...children);
    };
    const announcement = () => {
    const data = readout();
    if (!data) return '';
    const values = data.rows.map((row) => `${row.name} ${row.value}`).join(', ');
    return data.title ? t('chartPoint', { x: data.title, values }) : values;
    };
    const showCursor = (fromKeyboard, pointer = null) => {
    if (!current || cursor.index < 0 || cursor.index >= pointCount()) {
    hideCursor();
    return;
    }
    const lay = current;
    let anchor;
    dotsFor(null);
    if (lay.kind === 'radial') {
    const slice = lay.slices[cursor.index];
    const mid = (slice.a0 + slice.a1) / 2;
    const r = (slice.inner + slice.outer) / 2;
    anchor = { x: lay.cx + r * Math.sin(mid), y: lay.cy - r * Math.cos(mid) };
    marks = { series: -1, slice: cursor.index };
    gLines.querySelectorAll('.ds-chart__slice').forEach((node, i) => node.classList.toggle('ds-chart__slice--active', i === cursor.index));
    attrs(cursorLine, { x1: null, x2: null, y1: null, y2: null });
    cursorLine.classList.remove('ds-chart__cursor--on');
    } else {
    const pos = lay.positions[cursor.index];
    const { plot } = lay;
    const columnsOnly = lay.series.every((entry) => entry.mark === 'column');
    if (columnsOnly) {
    const half = (lay.band || 0) / 2;
    attrs(hoverRect, lay.horizontal
    ? { x: round(plot.x), y: round(pos - half), width: round(plot.width), height: round(half * 2) }
    : { x: round(pos - half), y: round(plot.y), width: round(half * 2), height: round(plot.height) });
    hoverRect.classList.add('ds-chart__band--on');
    cursorLine.classList.remove('ds-chart__cursor--on');
    } else {
    attrs(cursorLine, lay.horizontal ? { x1: round(plot.x), x2: round(plot.x + plot.width), y1: round(pos), y2: round(pos) } : { x1: round(pos), x2: round(pos), y1: round(plot.y), y2: round(plot.y + plot.height) });
    cursorLine.classList.add('ds-chart__cursor--on');
    hoverRect.classList.remove('ds-chart__band--on');
    }
    dotsFor(cursor.index);
    marks = { series: cursor.series, slice: -1 };
    for (const g of root.querySelectorAll('.ds-chart__bars > [data-series], .ds-chart__lines > [data-series], .ds-chart__areas > [data-series]')) {
    g.classList.toggle('ds-chart__series--dim', cursor.series >= 0 && Number(g.getAttribute('data-series')) !== cursor.series);
    }
    const ys = lay.series.filter((entry) => entry.visible && (cursor.series < 0 || entry.index === cursor.series)).map((entry) => (lay.horizontal ? entry.points[cursor.index]?.x : entry.points[cursor.index]?.y)).filter(finite);
    const across = pointer ? (lay.horizontal ? pointer.x : pointer.y) : ys.length ? Math.min(...ys) : plot.y + plot.height / 2;
    anchor = lay.horizontal ? { x: across, y: pos } : { x: pos, y: across };
    }
    if (spec.tooltip) {
    const data = readout();
    if (data) {
    fillTip(data);
    tip.setAttribute('data-state', 'open');
    const tipBox = { width: tip.offsetWidth, height: tip.offsetHeight };
    const bounds = { left: 0, top: 0, right: size.width, bottom: size.height };
    const rect = { left: anchor.x, right: anchor.x, top: anchor.y, bottom: anchor.y };
    const placed = computePlacement(rect, tipBox, bounds, { side: 'end', align: 'center', offset: 12, padding: 0, flip: 'room', size: false });
    const x = clamp(placed.x, 0, Math.max(0, size.width - tipBox.width));
    const y = clamp(placed.y, 0, Math.max(0, size.height - tipBox.height));
    tip.style.translate = `${round(x)}px ${round(y)}px`;
    }
    }
    if (fromKeyboard) announce(announcement());
    };
    const dotsFor = (index) => {
    if (index === null || !current?.series) {
    gDots.replaceChildren();
    return;
    }
    const entries = current.series.filter((entry) => entry.visible && entry.mark !== 'column' && (cursor.series < 0 || entry.index === cursor.series));
    keyed(gDots, entries, (entry) => entry.index, () => svg(doc, 'g')).forEach(({ el: g, item: entry }) => {
    attrs(g, { class: seriesClass('ds-chart__series', entry) });
    const point = shown.get(entry.index)?.get(spec.keys[index]) ?? entry.points[index];
    const marker = g.firstElementChild ?? g.appendChild(svg(doc, 'path', {}, 'ds-chart__dot'));
    if (!point || !finite(point.y) || !finite(point.x)) {
    attrs(marker, { d: null });
    return;
    }
    attrs(marker, { d: MARKERS[Math.max(0, entry.slot) % MARKERS.length](current.metrics.marker / 2 + 1), transform: `translate(${round(point.x)} ${round(point.y)})` });
    });
    };
    const hideCursor = () => {
    marks = { series: -1, slice: -1 };
    tip.removeAttribute('data-state');
    cursorLine.classList.remove('ds-chart__cursor--on');
    hoverRect.classList.remove('ds-chart__band--on');
    gLines.querySelectorAll('.ds-chart__slice--active').forEach((node) => node.classList.remove('ds-chart__slice--active'));
    root.querySelectorAll('.ds-chart__series--dim').forEach((node) => node.classList.remove('ds-chart__series--dim'));
    gDots.replaceChildren();
    };
    const moveTo = (index, series, fromKeyboard, pointer = null) => {
    const count = pointCount();
    if (!count) return;
    const next = clamp(index, 0, count - 1);
    const changed = next !== cursor.index || series !== cursor.series;
    cursor = { index: next, series, active: true };
    showCursor(fromKeyboard, pointer);
    if (changed) emitOut('point', pointDetail());
    };
    const pointerIndex = (event) => {
    const box = frame.getBoundingClientRect();
    const x = event.clientX - box.left;
    const y = event.clientY - box.top;
    if (!current) return null;
    if (current.kind === 'radial') {
    const dx = x - current.cx;
    const dy = y - current.cy;
    const r = Math.hypot(dx, dy);
    const hit = current.type === 'ring'
    ? current.slices.findIndex((slice) => r >= slice.inner - 2 && r <= slice.outer + 2)
    : r <= current.outer + 2 && r >= current.inner - 2 ? sliceAt(current.slices, Math.atan2(dx, -dy)) : -1;
    return hit < 0 ? null : { index: hit, x, y };
    }
    if (!current.plot) return null;
    const { plot } = current;
    if (x < plot.x - 8 || x > plot.x + plot.width + 8 || y < plot.y - 8 || y > plot.y + plot.height + 8) return null;
    const index = nearest(current.positions, current.horizontal ? y : x);
    return index < 0 ? null : { index, x, y };
    };
    const options = { signal };
    const pointerEvents = () => {
    if (destroyed) return;
    frame.addEventListener('pointermove', (event) => {
    if (!spec.tooltip || viewMode !== 'chart') return;
    const hit = pointerIndex(event);
    if (!hit) {
    if (!frame.contains(doc.activeElement)) hideCursor();
    return;
    }
    moveTo(hit.index, -1, false, { x: hit.x, y: hit.y });
    }, options);
    frame.addEventListener('pointerleave', (event) => {
    if (event.pointerType === 'touch') return;
    if (!frame.matches(':focus-visible')) {
    cursor.active = false;
    hideCursor();
    } else showCursor(false);
    }, options);
    frame.addEventListener('pointerdown', (event) => {
    if (event.pointerType !== 'touch' || !spec.tooltip) return;
    const hit = pointerIndex(event);
    if (hit) moveTo(hit.index, -1, false, { x: hit.x, y: hit.y });
    }, options);
    };
    queueMicrotask(pointerEvents);
    frame.addEventListener('click', (event) => {
    const hit = pointerIndex(event);
    if (!hit || spec.kind === 'gauge' || spec.kind === 'rows') return;
    cursor = { index: hit.index, series: -1, active: true };
    emitOut('select', pointDetail());
    }, options);
    frame.addEventListener('focus', () => {
    if (spec.kind === 'gauge' || spec.kind === 'rows') return;
    if (frame.matches(':focus-visible') || cursor.index >= 0) {
    const count = pointCount();
    moveTo(cursor.index >= 0 ? cursor.index : count - 1, cursor.series, true);
    }
    }, options);
    frame.addEventListener('blur', () => {
    cursor.active = false;
    hideCursor();
    }, options);
    frame.addEventListener('keydown', (event) => {
    if (!KEYS.has(event.key) || event.altKey || event.ctrlKey || event.metaKey) return;
    if (spec.kind === 'gauge' || spec.kind === 'rows') return;
    const count = pointCount();
    if (!count) return;
    if (event.key === 'Escape') {
    if (!cursor.active) return;
    cursor.active = false;
    hideCursor();
    event.preventDefault();
    return;
    }
    event.preventDefault();
    const radial = current?.kind === 'radial';
    const page = Math.max(1, Math.round(count / 10));
    let { index, series } = cursor;
    if (index < 0) index = count - 1;
    const visible = visibleSeries().map(({ index: i }) => i);
    const prev = radial || current?.horizontal ? ['ArrowLeft', 'ArrowUp'] : ['ArrowLeft'];
    const next = radial || current?.horizontal ? ['ArrowRight', 'ArrowDown'] : ['ArrowRight'];
    if (event.key === 'Enter') {
    emitOut('select', pointDetail());
    return;
    }
    if (prev.includes(event.key)) index -= 1;
    else if (next.includes(event.key)) index += 1;
    else if (event.key === 'Home') index = 0;
    else if (event.key === 'End') index = count - 1;
    else if (event.key === 'PageUp') index -= page;
    else if (event.key === 'PageDown') index += page;
    else if (!radial && visible.length > 1 && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
    const order = [-1, ...visible];
    const at = order.indexOf(series);
    series = order[(at + (event.key === 'ArrowDown' ? 1 : order.length - 1)) % order.length];
    }
    moveTo(clamp(index, 0, count - 1), series, true);
    }, options);
    const slicesToggle = () => spec.kind === 'radial' && spec.type !== 'ring';
    const toggleSlice = (slice, visible) => {
    const want = visible === undefined ? !slice.visible : Boolean(visible);
    if (want === slice.visible) return slice.visible;
    if (!want && (current?.slices ?? []).filter((item) => item.visible).length <= 1) return slice.visible;
    if (want) hiddenSlices.delete(slice.key);
    else hiddenSlices.add(slice.key);
    render(true);
    announce(t(want ? 'chartShown' : 'chartHidden', { series: slice.name }));
    emitOut('toggle', { series: slice.index, name: slice.name, visible: want });
    return want;
    };
    legend.addEventListener('click', (event) => {
    const button = event.target.closest?.('.ds-chart__key');
    if (!button || !legend.contains(button)) return;
    const key = button.getAttribute('data-series');
    if (slicesToggle()) {
    const slice = (current?.slices ?? []).find((item) => item.key === key);
    if (slice) toggleSlice(slice);
    return;
    }
    api.toggle(Number(key));
    }, options);
    toggle.addEventListener('click', () => api.view(viewMode === 'chart' ? 'table' : 'chart'), options);
    let resizer = null;
    if (typeof view?.ResizeObserver === 'function') {
    resizer = new view.ResizeObserver(() => {
    if (spec.kind === 'rows' || viewMode !== 'chart') return;
    const rect = frame.getBoundingClientRect();
    if (Math.round(rect.width) === size.width && Math.round(rect.height) === size.height) return;
    if (rect.width > 0) render(false);
    });
    resizer.observe(frame);
    }
    let watcher = null;
    const watchTable = () => {
    watcher?.disconnect();
    const table = sourceTable(el);
    if (!table || typeof view?.MutationObserver !== 'function') return;
    let queued = false;
    watcher = new view.MutationObserver(() => {
    if (queued) return;
    queued = true;
    queueMicrotask(() => {
    queued = false;
    if (!destroyed) apply(specOf(el), true);
    });
    });
    watcher.observe(table, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['data-value', 'datetime'] });
    };
    const apply = (next, animate) => {
    input = { ...(next ?? {}) };
    const before = spec;
    spec = normalize(input, before);
    if (before.type !== spec.type || before.kind !== spec.kind) {
    shown = new Map();
    arcsShown = new Map();
    }
    if (cursor.index >= pointCount()) cursor.index = -1;
    render(animate);
    };
    const api = {
    get() {
    return { ...input, type: spec.type, series: spec.series.map((entry) => ({ ...((input.series ?? [])[spec.series.indexOf(entry)] ?? {}), name: entry.name, values: [...entry.values], visible: entry.visible })), labels: [...spec.labels] };
    },
    set(next) {
    apply({ ...input, ...(next ?? {}) }, true);
    emitOut('change', { spec: api.get() });
    },
    on(type, fn) {
    if (!listeners.has(type)) listeners.set(type, new Set());
    listeners.get(type).add(fn);
    return () => listeners.get(type)?.delete(fn);
    },
    toggle(series, visible) {
    if (slicesToggle()) {
    const slices = current?.slices ?? [];
    const slice = typeof series === 'number' ? slices[series] : slices.find((item) => item.name === series || item.key === String(series));
    return slice ? toggleSlice(slice, visible) : false;
    }
    const index = typeof series === 'number' ? series : spec.series.findIndex((entry) => entry.name === series);
    const entry = spec.series[index];
    if (!entry) return false;
    const want = visible === undefined ? !entry.visible : Boolean(visible);
    if (want === entry.visible) return entry.visible;
    if (!want && spec.series.filter((item) => item.visible).length <= 1) return entry.visible;
    entry.visible = want;
    if (input.series?.[index]) input.series[index] = { ...input.series[index], visible: want };
    if (cursor.series === index) cursor.series = -1;
    render(true);
    announce(t(want ? 'chartShown' : 'chartHidden', { series: entry.name }));
    emitOut('toggle', { series: index, name: entry.name, visible: want });
    return want;
    },
    view(mode) {
    if (mode !== 'chart' && mode !== 'table') return viewMode;
    if (mode === viewMode) return viewMode;
    viewMode = mode;
    el.setAttribute('data-state', mode);
    toggle.setAttribute('aria-pressed', String(mode === 'table'));
    hideCursor();
    emitOut('view', { view: mode });
    if (mode === 'chart') render(false);
    return viewMode;
    },
    focus(index, series = -1) {
    moveTo(index, series, true);
    },
    update() {
    apply(specOf(el), true);
    watchTable();
    },
    destroy() {
    if (destroyed) return;
    destroyed = true;
    stopMorph();
    resizer?.disconnect();
    watcher?.disconnect();
    listeners.clear();
    tools.remove();
    legend.remove();
    frame.remove();
    tableBox.remove();
    for (const name of applied) el.classList.remove(name);
    el.removeAttribute('data-state');
    },
    };
    mount();
    watchTable();
    render(true);
    return api;
    }
    define('chart', {
    attribute: 'data-ds-chart',
    attributes: ['data-ds-config'],
    init(el, ctx) {
    return controller(el, specOf(el), ctx.signal);
    },
    });
    function create(el, spec) {
    return controller(el, spec ?? specOf(el), undefined);
    }

    return Object.freeze({
      ticks: ticks,
      timeTicks: timeTicks,
      scale: scale,
      path: path,
      stack: stack,
      nearest: nearest,
      parse: parse,
      layout: layout,
      create: create
    });
  });

  add('composer', ['query', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const leave = DS.motion.leave, enter = DS.motion.enter;
    const autosize = DS.dom.autosize, dropzone = DS.dom.dropzone;
    const closeGlyph = DS.icons.close, fileGlyph = DS.icons.file, fileImage = DS.icons.fileImage, iconElement = DS.icons.iconElement;
    const computePlacement = DS.position.computePlacement, pxOf = DS.position.pxOf, viewportBounds = DS.position.viewportBounds;
    const openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close;
    const announce = DS.a11y.announce;
    const bytes = DS.format.bytes, list = DS.format.list, number = DS.format.number;
    const openStore = DS.store.open;
    const DRAFTS = 'composer';
    const EVENTS = new Set(['send', 'stop', 'attach', 'remove', 'command', 'mention', 'query']);
    const MIRRORED = ['box-sizing', 'width', 'border-top-width', 'border-right-width', 'border-bottom-width', 'border-left-width',
    'padding-top', 'padding-right', 'padding-bottom', 'padding-left', 'font-style', 'font-variant', 'font-weight', 'font-stretch',
    'font-size', 'font-size-adjust', 'line-height', 'font-family', 'text-align', 'text-transform', 'text-indent',
    'letter-spacing', 'word-spacing', 'tab-size', 'direction'];
    let sequence = 0;
    function query(text, caret, triggers = ['/', '@']) {
    const value = String(text ?? '');
    const end = Math.max(0, Math.min(Number(caret) || 0, value.length));
    let start = end;
    while (start > 0 && !/\s/.test(value[start - 1])) start -= 1;
    const word = value.slice(start, end);
    const trigger = triggers.find((item) => item && word.startsWith(item));
    if (!trigger) return null;
    const rest = word.slice(trigger.length);
    if (triggers.some((item) => item && rest.includes(item))) return null;
    if (trigger === '/' && value.slice(0, start).trim() !== '') return null;
    return { trigger, query: rest, start, end };
    }
    function caretAt(textarea, index) {
    const doc = textarea.ownerDocument;
    const view = doc.defaultView;
    const style = view.getComputedStyle(textarea);
    const mirror = doc.createElement('div');
    mirror.setAttribute('aria-hidden', 'true');
    mirror.setAttribute('data-ds-keep', '');
    for (const name of MIRRORED) mirror.style.setProperty(name, style.getPropertyValue(name));
    mirror.style.setProperty('position', 'absolute');
    mirror.style.setProperty('visibility', 'hidden');
    mirror.style.setProperty('top', '0');
    mirror.style.setProperty('left', '0');
    mirror.style.setProperty('overflow', 'hidden');
    mirror.style.setProperty('white-space', 'pre-wrap');
    mirror.style.setProperty('overflow-wrap', 'break-word');
    mirror.textContent = textarea.value.slice(0, index);
    const mark = doc.createElement('span');
    mark.textContent = textarea.value.slice(index) || '.';
    mirror.append(mark);
    (textarea.parentNode ?? doc.body).append(mirror);
    const line = parseFloat(style.lineHeight);
    const box = textarea.getBoundingClientRect();
    const point = {
    x: box.left + mark.offsetLeft - textarea.scrollLeft,
    y: box.top + mark.offsetTop - textarea.scrollTop,
    height: Number.isFinite(line) ? line : mark.offsetHeight,
    };
    mirror.remove();
    return point;
    }
    function labelOf(option) {
    return (option.getAttribute('data-label') ?? option.querySelector('.ds-composer__option-label')?.textContent ?? option.textContent ?? '').trim();
    }
    function valueOf(option) {
    return option.getAttribute('data-value') ?? labelOf(option);
    }
    function enterSends(el, mode) {
    const choice = mode ?? el.getAttribute('data-ds-composer-enter');
    if (choice === 'send') return true;
    if (choice === 'newline') return false;
    const view = el.ownerDocument?.defaultView;
    return !view?.matchMedia || view.matchMedia('(any-pointer: fine)').matches;
    }
    function controller(el, options, ctx) {
    const doc = el.ownerDocument;
    const signal = ctx.signal;
    const textarea = (el.querySelector('.ds-composer__input, textarea'));
    const listeners = new Map();
    const attachments = [];
    const chosen = [];
    let busy = Boolean(options.busy);
    let draftKey = options.draft ?? null;
    let enterMode = options.enter ?? null;
    let store = null;
    let active = null;
    let token = null;
    let dismissed = -1;
    let watching = null;
    let heard = -1;
    const sizing = textarea ? autosize(textarea) : null;
    const fire = (type, detail, cancelable = false) => {
    const go = emit(el, type, detail, { cancelable });
    for (const fn of listeners.get(type) ?? []) fn(detail);
    return go;
    };
    const submit = () => el.querySelector('button[type="submit"], .ds-btn[type="submit"]') ?? el.querySelector('button:not([type]):not([data-ds-composer-attach])');
    const picker = () => (el.querySelector('.ds-composer__picker, input[type="file"]'));
    const tray = () => el.querySelector('.ds-composer__attachments');
    const palettes = () => (Array.from(el.querySelectorAll('.ds-composer__palette')));
    const triggers = () => palettes().map((palette) => palette.getAttribute('data-ds-composer-trigger') ?? '').filter(Boolean);
    const paletteFor = (trigger) => palettes().find((palette) => palette.getAttribute('data-ds-composer-trigger') === trigger) ?? null;
    const isOpen = (palette) => Boolean(palette && !palette.hidden && palette.getAttribute('data-state') === 'open');
    const submitLabel = { name: null, tip: null };
    function syncButton() {
    const button = submit();
    if (!button) return;
    if (submitLabel.name === null) {
    submitLabel.name = button.getAttribute('aria-label') ?? t('send');
    submitLabel.tip = button.getAttribute('data-tip');
    }
    const name = busy ? t('stop') : submitLabel.name;
    if (button.getAttribute('aria-label') !== name) button.setAttribute('aria-label', name);
    if (submitLabel.tip !== null) button.setAttribute('data-tip', busy ? t('stop') : submitLabel.tip);
    const swap = button.querySelector('.ds-icon-swap');
    if (swap) swap.setAttribute('data-swap', busy ? 'b' : 'a');
    const empty = !busy && !(textarea?.value.trim()) && attachments.length === 0;
    if (empty) button.setAttribute('aria-disabled', 'true');
    else button.removeAttribute('aria-disabled');
    if (busy) el.setAttribute('data-state', 'busy');
    else el.removeAttribute('data-state');
    }
    function draftStore() {
    if (!draftKey) return null;
    store ??= openStore(DRAFTS);
    return store;
    }
    function saveDraft() {
    const drafts = draftStore();
    if (!drafts || !textarea) return;
    if (textarea.value) drafts.set(draftKey, textarea.value);
    else drafts.remove(draftKey);
    }
    function switchDraft(next) {
    saveDraft();
    draftKey = next;
    for (const palette of palettes()) closePalette(palette);
    token = null;
    dismissed = -1;
    chosen.length = 0;
    clearAttachments();
    if (textarea) {
    textarea.value = '';
    restoreDraft();
    sizing?.update();
    }
    }
    function restoreDraft() {
    const drafts = draftStore();
    if (!drafts || !textarea || textarea.value) return;
    const text = drafts.get(draftKey, '');
    if (typeof text === 'string' && text) {
    textarea.value = text;
    sizing?.update();
    }
    }
    function optionsIn(palette) {
    return (Array.from(palette.querySelectorAll('.ds-composer__option, [role="option"]')));
    }
    const usable = (option) => !option.hidden && option.getAttribute('aria-disabled') !== 'true';
    function activate(option) {
    if (active && active !== option) active.setAttribute('aria-selected', 'false');
    active = option;
    if (!textarea) return;
    if (option) {
    if (!option.id) option.id = `ds-composer-option-${sequence += 1}`;
    option.setAttribute('aria-selected', 'true');
    textarea.setAttribute('aria-activedescendant', option.id);
    option.scrollIntoView?.({ block: 'nearest' });
    } else textarea.removeAttribute('aria-activedescendant');
    }
    function filter(palette, text) {
    const needle = text.toLowerCase();
    const own = palette.getAttribute('data-ds-composer-filter') !== 'off';
    let first = null;
    let current = null;
    let count = 0;
    for (const option of optionsIn(palette)) {
    option.setAttribute('aria-selected', 'false');
    if (own) option.hidden = !labelOf(option).toLowerCase().replace(/^[/@#:]/, '').includes(needle.replace(/^[/@#:]/, ''));
    if (!usable(option)) continue;
    count += 1;
    first ??= option;
    if (option === active) current = option;
    }
    let none = palette.querySelector('.ds-composer__empty');
    if (!first && !none) {
    none = doc.createElement('div');
    none.className = 'ds-composer__empty';
    none.textContent = t('noResults');
    palette.append(none);
    }
    if (none) (none).hidden = Boolean(first);
    activate(current ?? first);
    return count;
    }
    function sayCount(count) {
    if (count === heard) return;
    heard = count;
    announce(count ? t('resultCount', { count: number(count) }) : t('noResults'), { key: 'composer-count' });
    }
    function watch(palette) {
    watching?.abort();
    const current = new AbortController();
    watching = current;
    const view = doc.defaultView;
    const signal = current.signal;
    doc.addEventListener('pointerdown', onDocDown, { capture: true, signal });
    let frame = 0;
    const again = () => {
    if (frame) return;
    frame = view.requestAnimationFrame(() => {
    frame = 0;
    if (watching === current && isOpen(palette)) place(palette);
    });
    };
    const onScroll = (event) => {
    if (event.target?.nodeType === 1 && palette.contains(event.target)) return;
    again();
    };
    view.addEventListener('scroll', onScroll, { signal, capture: true, passive: true });
    view.addEventListener('resize', again, { signal });
    view.visualViewport?.addEventListener('resize', again, { signal });
    view.visualViewport?.addEventListener('scroll', again, { signal });
    const sizes = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(again) : null;
    sizes?.observe(palette);
    signal.addEventListener('abort', () => {
    sizes?.disconnect();
    if (frame) view.cancelAnimationFrame(frame);
    }, { once: true });
    }
    function unwatch() {
    watching?.abort();
    watching = null;
    }
    function place(palette) {
    if (!textarea || !token) return;
    const caret = caretAt(textarea, token.start);
    const style = palette.style;
    style.removeProperty('max-height');
    const size = { width: palette.offsetWidth, height: palette.offsetHeight };
    const rect = { left: caret.x, right: caret.x, top: caret.y, bottom: caret.y + caret.height };
    const result = computePlacement(rect, size, viewportBounds(palette), {
    side: 'top', align: 'start', offset: pxOf(palette, '--ds-space-1', 4), padding: pxOf(palette, '--ds-space-2', 8), flip: 'room',
    });
    if (result.length < size.height) style.setProperty('max-height', `${Math.round(result.length)}px`);
    style.setProperty('--ds-composer-palette-x', `${Math.round(result.x)}px`);
    style.setProperty('--ds-composer-palette-y', `${Math.round(result.y)}px`);
    if (result.side === 'bottom') palette.setAttribute('data-placement', 'bottom');
    else palette.removeAttribute('data-placement');
    return { x: caret.x, y: result.side === 'bottom' ? caret.y + caret.height : caret.y };
    }
    function openPalette(palette) {
    if (!textarea) return;
    if (!palette.id) palette.id = `ds-composer-palette-${sequence += 1}`;
    textarea.setAttribute('aria-controls', palette.id);
    const open = isOpen(palette);
    if (!open) heard = -1;
    sayCount(filter(palette, token?.query ?? ''));
    if (open) {
    place(palette);
    return;
    }
    for (const other of palettes()) if (other !== palette && isOpen(other)) closePalette(other);
    palette.hidden = false;
    if (palette.hasAttribute('popover')) {
    try {
    if (!palette.matches(':popover-open')) palette.showPopover();
    } catch {
    }
    }
    place(palette);
    openOverlay(palette, { opener: textarea });
    watch(palette);
    }
    function closePalette(palette, reason) {
    if (!palette || palette.hidden) return;
    if (reason === 'escape' && token) dismissed = token.start;
    activate(null);
    unwatch();
    textarea?.removeAttribute('aria-controls');
    if (palette.getAttribute('data-state') === 'closed') return;
    closeOverlay(palette).then(() => {
    if (palette.getAttribute('data-state') === 'open' || !palette.hidden) return;
    try {
    if (palette.matches(':popover-open')) palette.hidePopover();
    } catch {
    }
    });
    }
    function openPaletteNow() {
    return palettes().find(isOpen) ?? null;
    }
    function review() {
    if (!textarea) return;
    const caret = textarea.selectionStart === textarea.selectionEnd ? textarea.selectionEnd : -1;
    const found = caret < 0 || doc.activeElement !== textarea ? null : query(textarea.value, caret, triggers());
    if (!found) dismissed = -1;
    token = found && found.start !== dismissed ? found : null;
    const palette = token ? paletteFor(token.trigger) : null;
    for (const other of palettes()) if (other !== palette && isOpen(other)) closePalette(other);
    if (!palette) return;
    const before = palette.getAttribute('data-ds-query');
    if (before !== token.query) {
    palette.setAttribute('data-ds-query', token.query);
    fire('query', { trigger: token.trigger, query: token.query });
    }
    openPalette(palette);
    }
    function choose(option) {
    if (!textarea || !token || !option || !usable(option)) return;
    const palette = option.closest('.ds-composer__palette');
    const value = valueOf(option);
    const trigger = token.trigger;
    const insert = `${value} `;
    textarea.setRangeText(insert, token.start, token.end, 'end');
    const detail = { value, id: option.getAttribute('data-id') ?? value, option };
    chosen.push({ trigger, ...detail });
    token = null;
    closePalette(palette);
    textarea.dispatchEvent(new (doc.defaultView?.Event ?? Event)('input', { bubbles: true }));
    fire(trigger === '/' ? 'command' : 'mention', detail);
    }
    function move(step) {
    const palette = openPaletteNow();
    if (!palette) return;
    const items = optionsIn(palette).filter(usable);
    if (!items.length) return;
    const index = items.indexOf(active);
    const next = index < 0 ? (step > 0 ? 0 : items.length - 1) : (index + step + items.length) % items.length;
    activate(items[next]);
    }
    function chipOf(item) {
    const chip = doc.createElement('li');
    chip.className = 'ds-composer__attachment';
    chip.setAttribute('data-ds-key', item.id);
    chip.append(iconElement(/^image\//.test(item.type) ? fileImage : fileGlyph, 18, undefined, doc));
    const name = doc.createElement('span');
    name.className = 'ds-composer__attachment-name';
    name.textContent = item.name;
    name.title = item.name;
    const size = doc.createElement('span');
    size.className = 'ds-composer__attachment-size';
    size.textContent = bytes(item.size);
    const remove = doc.createElement('button');
    remove.type = 'button';
    remove.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-xs';
    remove.setAttribute('aria-label', t('removeNamed', { name: item.name }));
    remove.setAttribute('data-ds-composer-remove', item.id);
    remove.append(iconElement(closeGlyph, 16, undefined, doc));
    chip.append(name, size, remove);
    return chip;
    }
    function add(files) {
    const list = Array.from(files ?? []).filter((item) => item && typeof item.name === 'string');
    if (!list.length) return [];
    const holder = tray();
    const added = list.map((file) => ({ id: `ds-attachment-${sequence += 1}`, name: file.name, size: Number(file.size) || 0, type: file.type ?? '', file }));
    attachments.push(...added);
    if (holder) {
    for (const item of added) {
    const chip = chipOf(item);
    holder.append(chip);
    enter(chip, 'pop');
    }
    }
    syncButton();
    announce(t('filesAttached', { names: listNames(added) }));
    fire('attach', { files: added.map((item) => item.file), attachments: added.map(copyOf) });
    return added.map(copyOf);
    }
    const chipFor = (id) => Array.from(tray()?.children ?? []).find((chip) => chip.getAttribute('data-ds-key') === id) ?? null;
    function remove(id) {
    const index = attachments.findIndex((item) => item.id === id);
    if (index < 0) return;
    const [item] = attachments.splice(index, 1);
    const chip = chipFor(id);
    if (chip) {
    const chips = Array.from(chip.parentElement.children).filter((other) => other !== chip && !other.hasAttribute('data-ds-leaving'));
    const after = chips.find((other) => chip.compareDocumentPosition(other) & 4) ?? chips.at(-1);
    const focusNext = () => after?.querySelector('button') ?? textarea;
    leave(chip, 'pop', { focus: focusNext });
    }
    syncButton();
    announce(t('fileRemoved', { name: item.name }));
    fire('remove', { attachment: copyOf(item) });
    }
    function clearAttachments() {
    for (const item of attachments.splice(0)) {
    const chip = chipFor(item.id);
    if (chip) leave(chip, 'pop', { focus: textarea });
    }
    }
    const copyOf = (item) => ({ id: item.id, name: item.name, size: item.size, type: item.type, file: item.file });
    const listNames = (items) => list(items.map((item) => item.name));
    function send() {
    if (!textarea || busy) return null;
    const text = textarea.value;
    if (!text.trim() && !attachments.length) return null;
    const mentions = chosen.filter((item) => item.trigger !== '/' && text.includes(item.value)).map(({ value, id }) => ({ value, id }));
    const command = chosen.find((item) => item.trigger === '/' && text.trimStart().startsWith(item.value))?.value ?? null;
    const detail = { text, attachments: attachments.map(copyOf), mentions, command };
    if (!fire('send', detail, true)) return null;
    textarea.value = '';
    chosen.length = 0;
    clearAttachments();
    sizing?.update();
    saveDraft();
    syncButton();
    for (const palette of palettes()) closePalette(palette);
    if (doc.activeElement !== textarea && el.contains(doc.activeElement)) textarea.focus({ preventScroll: true });
    return detail;
    }
    const onKey = ( event) => {
    if (event.target !== textarea || event.defaultPrevented) return;
    if (event.isComposing || event.keyCode === 229) return;
    const palette = openPaletteNow();
    if (palette) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    move(event.key === 'ArrowDown' ? 1 : -1);
    return;
    }
    if ((event.key === 'Enter' || event.key === 'Tab') && !event.shiftKey && !event.altKey && active) {
    event.preventDefault();
    choose(active);
    return;
    }
    }
    if (event.key !== 'Enter' || event.shiftKey || event.altKey) return;
    const mod = event.ctrlKey || event.metaKey;
    if (!mod && !enterSends(el, enterMode)) return;
    event.preventDefault();
    send();
    };
    const onInput = (event) => {
    if (event.target !== textarea) return;
    saveDraft();
    syncButton();
    review();
    };
    const onSubmit = (event) => {
    event.preventDefault();
    if (busy) {
    fire('stop', {});
    return;
    }
    send();
    };
    const onClick = (event) => {
    const target = (event.target);
    const attach = target.closest?.('[data-ds-composer-attach]');
    if (attach && el.contains(attach)) {
    picker()?.click();
    return;
    }
    const removing = target.closest?.('[data-ds-composer-remove]');
    if (removing && el.contains(removing)) {
    remove(removing.getAttribute('data-ds-composer-remove'));
    return;
    }
    const button = target.closest?.('button[type="submit"]');
    if (button && el.contains(button) && el.localName !== 'form') {
    onSubmit(event);
    return;
    }
    const option = target.closest?.('.ds-composer__option, [role="option"]');
    if (option && option.closest('.ds-composer__palette')?.parentElement && el.contains(option)) {
    choose(option);
    textarea?.focus({ preventScroll: true });
    return;
    }
    if (target === textarea) review();
    };
    const onDown = (event) => {
    if (event.target.closest?.('.ds-composer__palette')) event.preventDefault();
    };
    const onDocDown = (event) => {
    const palette = openPaletteNow();
    if (!palette) return;
    const target = event.target;
    if (palette.contains(target) || target === textarea) return;
    closePalette(palette);
    };
    const onCaret = (event) => {
    if (event.target !== textarea) return;
    if (['ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key) || (/^Arrow/.test(event.key ?? '') && !openPaletteNow())) review();
    };
    const onBlur = (event) => {
    if (event.target !== textarea) return;
    for (const palette of palettes()) if (isOpen(palette)) closePalette(palette);
    };
    const onClose = (event) => {
    const palette = event.target;
    if (!palette?.classList?.contains('ds-composer__palette')) return;
    if (event.detail?.reason === 'escape' && token) dismissed = token.start;
    activate(null);
    unwatch();
    textarea?.removeAttribute('aria-controls');
    };
    el.addEventListener('keydown', onKey, { signal });
    el.addEventListener('keyup', onCaret, { signal });
    el.addEventListener('input', onInput, { signal });
    el.addEventListener('submit', onSubmit, { signal });
    el.addEventListener('click', onClick, { signal });
    el.addEventListener('mousedown', onDown, { signal });
    el.addEventListener('focusout', onBlur, { signal });
    el.addEventListener('ds:close', onClose, { signal });
    const drops = dropzone(el, { onFiles: (files) => add(files), paste: true, input: picker() });
    if (textarea) textarea.setAttribute('aria-autocomplete', palettes().length ? 'list' : 'none');
    for (const palette of palettes()) {
    if (!palette.hasAttribute('popover')) palette.setAttribute('popover', 'manual');
    for (const option of optionsIn(palette)) option.setAttribute('aria-selected', 'false');
    }
    restoreDraft();
    syncButton();
    const handle = {
    get() {
    return { text: textarea?.value ?? '', attachments: attachments.map(copyOf), busy };
    },
    set(value = {}) {
    if (typeof value === 'string') value = { text: value };
    if ('busy' in value) busy = Boolean(value.busy);
    if ('text' in value && textarea) {
    textarea.value = String(value.text ?? '');
    sizing?.update();
    saveDraft();
    }
    if (Array.isArray(value.attachments)) {
    clearAttachments();
    add(value.attachments);
    }
    syncButton();
    },
    on(type, fn) {
    if (!EVENTS.has(type) || typeof fn !== 'function') return () => {};
    if (!listeners.has(type)) listeners.set(type, new Set());
    listeners.get(type).add(fn);
    return () => listeners.get(type)?.delete(fn);
    },
    send,
    add,
    remove,
    focus() {
    textarea?.focus();
    },
    update(names = ['data-ds-composer-busy', 'data-ds-composer-draft', 'data-ds-composer-enter']) {
    const changed = new Set(names);
    if (changed.has('data-ds-composer-busy')) busy = el.hasAttribute('data-ds-composer-busy');
    if (changed.has('data-ds-composer-enter')) enterMode = el.getAttribute('data-ds-composer-enter');
    if (changed.has('data-ds-composer-draft')) {
    const next = el.getAttribute('data-ds-composer-draft') || null;
    if (next !== draftKey) switchDraft(next);
    }
    syncButton();
    },
    destroy() {
    listeners.clear();
    for (const palette of palettes()) closePalette(palette);
    unwatch();
    drops.destroy();
    sizing?.destroy();
    store?.destroy();
    store = null;
    },
    };
    return handle;
    }
    const controllers = new WeakMap();
    function controllerOf(el, options) {
    const held = controllers.get(el);
    if (held) return held;
    const abort = new AbortController();
    const handle = controller(el, options, { signal: abort.signal });
    const destroy = handle.destroy;
    handle.destroy = () => {
    if (controllers.get(el) !== handle) return;
    controllers.delete(el);
    destroy();
    abort.abort();
    };
    controllers.set(el, handle);
    return handle;
    }
    define('composer', {
    attribute: 'data-ds-composer',
    attributes: ['data-ds-composer-busy', 'data-ds-composer-draft', 'data-ds-composer-enter'],
    init(el) {
    return controllerOf(el, {
    busy: el.hasAttribute('data-ds-composer-busy'),
    draft: el.getAttribute('data-ds-composer-draft') || null,
    enter: el.getAttribute('data-ds-composer-enter'),
    });
    },
    });
    function create(el, options = {}) {
    return controllerOf(el, {
    busy: options.busy ?? el.hasAttribute('data-ds-composer-busy'),
    draft: options.draft ?? (el.getAttribute('data-ds-composer-draft') || null),
    enter: options.enter ?? el.getAttribute('data-ds-composer-enter'),
    });
    }

    return Object.freeze({
      query: query,
      create: create
    });
  });

  add('dialog', ['open', 'close', 'confirm', 'alert', 'prompt', 'form', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const busy = DS.motion.busy;
    const alertCircle = DS.icons.alertCircle, iconElement = DS.icons.iconElement;
    const watchScrolled = DS.dom.watchScrolled;
    const tabbables = DS.a11y.tabbables;
    const internals = DS.overlay.internals, openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close;
    const { overlays, activeIn, sharedOf, canFocus, arrowTarget, controllerOf, part } = internals;
    function open(element, options = {}) {
    return openOverlay(element, options);
    }
    function close(element, options = {}) {
    return closeOverlay(element, options);
    }
    function confirm(options = {}) {
    return (ask('confirm', options));
    }
    function alert(options = {}) {
    return (ask('alert', options));
    }
    function prompt(options = {}) {
    return (ask('prompt', options));
    }
    const declined = (kind) => (kind === 'confirm' ? false : kind === 'prompt' ? null : undefined);
    function ask(kind, options) {
    const opener = options.opener ?? null;
    const doc = opener?.ownerDocument ?? globalThis.document;
    if (!doc?.createElement || !doc.body) return Promise.resolve(declined(kind));
    const theme = opener?.closest?.('.ds-theme') ?? activeIn(doc.body)?.closest?.('.ds-theme') ?? doc.querySelector('.ds-theme');
    const home = !theme || theme === doc.documentElement ? doc.body : theme;
    const id = `ds-ask-${sharedOf(doc).seq += 1}`;
    const make = (tag, className, text) => {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = String(text);
    return node;
    };
    const native = typeof make('dialog').showModal === 'function';
    const dialog = make(native ? 'dialog' : 'div', 'ds-dialog');
    const box = make('div', 'ds-dialog__box');
    const labelled = native ? dialog : box;
    const role = kind === 'alert' || options.destructive ? 'alertdialog' : 'dialog';
    if (native) {
    if (role === 'alertdialog') dialog.setAttribute('role', role);
    } else {
    dialog.hidden = true;
    box.setAttribute('role', role);
    box.setAttribute('aria-modal', 'true');
    }
    const backdrop = make('div', 'ds-dialog__backdrop');
    backdrop.setAttribute('data-ds-close', '');
    const title = make('h2', 'ds-dialog__title', options.title ?? '');
    title.id = `${id}-title`;
    labelled.setAttribute('aria-labelledby', title.id);
    box.append(title);
    if (options.text) {
    const text = make('p', 'ds-dialog__text', options.text);
    text.id = `${id}-text`;
    labelled.setAttribute('aria-describedby', text.id);
    box.append(text);
    }
    const lines = Array.isArray(options.lines) ? options.lines.filter((line) => line != null && String(line) !== '') : [];
    if (lines.length) {
    const list = make('ul', 'ds-dialog__lines');
    for (const line of lines) list.append(make('li', '', line));
    box.append(list);
    }
    const facts = Array.isArray(options.facts) ? options.facts.filter((fact) => Array.isArray(fact) && fact.length >= 2) : [];
    if (facts.length) {
    const list = make('dl', 'ds-dialog__facts');
    for (const [term, value] of facts) {
    const row = make('div', '');
    row.append(make('dt', '', term), make('dd', '', value));
    list.append(row);
    }
    box.append(list);
    }
    let field = null;
    if (kind === 'prompt') {
    const wrap = make('div', 'ds-field');
    const label = make('label', 'ds-field__label', options.label ?? '');
    field = (make('input', 'ds-input'));
    field.id = `${id}-field`;
    label.htmlFor = field.id;
    const input = options.input ?? {};
    field.type = input.type ?? 'text';
    if (input.autocomplete) field.setAttribute('autocomplete', input.autocomplete);
    if (input.inputmode) field.setAttribute('inputmode', input.inputmode);
    if (input.required) field.required = true;
    if (typeof input.spellcheck === 'boolean') field.setAttribute('spellcheck', String(input.spellcheck));
    for (const name of ['autocapitalize', 'autocorrect', 'enterkeyhint']) {
    if (input[name]) field.setAttribute(name, input[name]);
    }
    field.value = options.value ?? '';
    field.setAttribute('autofocus', '');
    wrap.append(label, field);
    box.append(wrap);
    }
    const actions = make('div', 'ds-dialog__actions');
    const button = (className, text) => {
    const node = (make('button', className, text));
    node.type = 'button';
    return node;
    };
    let cancel = null;
    let confirmButton;
    if (kind === 'alert') {
    confirmButton = button('ds-btn ds-btn--filled', options.closeLabel ?? t('ok'));
    confirmButton.setAttribute('data-ds-close', '');
    confirmButton.setAttribute('autofocus', '');
    actions.append(confirmButton);
    } else {
    cancel = button('ds-btn ds-btn--text', options.cancelLabel ?? t('cancel'));
    cancel.setAttribute('data-ds-close', '');
    const label = options.confirmLabel ?? t('confirm');
    confirmButton = button(options.destructive ? 'ds-btn ds-btn--filled ds-tone-error' : 'ds-btn ds-btn--filled');
    const faces = make('span', 'ds-btn__faces');
    const rest = make('span', 'ds-btn__face', label);
    const working = make('span', 'ds-btn__face ds-btn__face--busy');
    working.setAttribute('aria-hidden', 'true');
    working.append(make('span', 'ds-spinner ds-spinner--ink ds-sm'), doc.createTextNode(label));
    faces.append(rest, working);
    confirmButton.append(faces);
    if (options.destructive) cancel.setAttribute('autofocus', '');
    else if (!field) confirmButton.setAttribute('autofocus', '');
    actions.append(cancel, confirmButton);
    }
    box.append(actions);
    dialog.append(backdrop, box);
    return new Promise((resolve) => {
    dialog.addEventListener('ds:close', (event) => {
    if (event.target !== dialog) return;
    const reason = (event).detail?.reason;
    resolve(reason === 'confirm' ? (kind === 'confirm' ? true : kind === 'prompt' ? field?.value ?? '' : undefined) : declined(kind));
    Promise.resolve(overlays.get(dialog)?.closing).then(() => {
    if (overlays.get(dialog)?.phase !== 'open') dialog.remove();
    });
    });
    if (kind === 'alert') {
    home.append(dialog);
    open(dialog, { opener });
    return;
    }
    let hint = null;
    const invalid = ( input) => {
    const text = String(options.requiredText ?? t('valueRequired'));
    if (!hint) {
    hint = make('small', 'ds-field__hint ds-field__hint--error');
    hint.id = `${id}-error`;
    hint.append(iconElement(alertCircle, 16, undefined, doc), doc.createTextNode(text));
    input.parentElement?.append(hint);
    }
    input.setAttribute('aria-invalid', 'true');
    input.setAttribute('aria-describedby', hint.id);
    input.focus();
    const speaker = part('toast');
    speaker?.announce(text, { assertive: true, region: speaker.regionOfTheme(doc, theme, home) });
    };
    const valid = () => {
    if (!field) return;
    field.removeAttribute('aria-invalid');
    field.removeAttribute('aria-describedby');
    hint?.remove();
    hint = null;
    };
    const attempt = async () => {
    if (dialog.hasAttribute('data-ds-busy') || overlays.get(dialog)?.phase !== 'open') return;
    if (field?.required && !field.value.trim()) {
    invalid(field);
    return;
    }
    valid();
    if (typeof options.onConfirm !== 'function') {
    close(dialog, { reason: 'confirm' });
    return;
    }
    dialog.setAttribute('data-ds-busy', '');
    cancel?.setAttribute('aria-disabled', 'true');
    if (field) field.readOnly = true;
    const settle = () => {
    dialog.removeAttribute('data-ds-busy');
    cancel?.removeAttribute('aria-disabled');
    if (field) field.readOnly = false;
    };
    try {
    await busy(confirmButton, () => options.onConfirm(field?.value ?? undefined), { done: false });
    } catch {
    settle();
    if (!dialog.isConnected) return;
    const speaker = part('toast');
    const region = speaker?.regionOfTheme(doc, theme, home);
    if (region) speaker.toast(region, options.errorText ?? t('actionFailed'), { kind: 'error' });
    confirmButton.focus({ preventScroll: true });
    return;
    }
    settle();
    close(dialog, { reason: 'confirm' });
    };
    confirmButton.addEventListener('click', ( event) => {
    if (event.detail > 1) return;
    attempt();
    });
    field?.addEventListener('keydown', ( event) => {
    if (event.key !== 'Enter' || event.isComposing || event.defaultPrevented) return;
    event.preventDefault();
    attempt();
    });
    field?.addEventListener('input', valid);
    home.append(dialog);
    open(dialog, { opener });
    });
    }
    function wireDialogList(dialog) {
    dialog.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    const list = (event.target)?.closest?.('.ds-dialog__list');
    if (!list || !dialog.contains(list)) return;
    const next = arrowTarget([...list.querySelectorAll('.ds-menu__item')].filter(canFocus), event);
    if (!next) return;
    event.preventDefault();
    next.focus();
    });
    }
    const forms = new WeakMap();
    function form(element, options = {}) {
    forms.set(element, { onSubmit: options.onSubmit, errorText: options.errorText });
    return controllerOf(element);
    }
    const TYPED = 'textarea, input:not([type="checkbox"], [type="radio"], [type="range"], [type="color"], [type="file"], [type="button"], [type="reset"], [type="submit"], [type="image"], [type="hidden"])';
    function holdForm(dialog, box, submit) {
    const undo = [() => dialog.removeAttribute('data-ds-busy')];
    dialog.setAttribute('data-ds-busy', '');
    for (const closer of box.querySelectorAll('[data-ds-close]')) {
    if (closer.getAttribute('aria-disabled') === 'true') continue;
    closer.setAttribute('aria-disabled', 'true');
    undo.push(() => closer.removeAttribute('aria-disabled'));
    }
    for (const control of box.elements) {
    if (control === submit || control.disabled || control.type === 'submit' || control.matches('fieldset, output, input[type="hidden"], [data-ds-close]')) continue;
    if (control.matches(TYPED)) {
    if (control.readOnly) continue;
    control.readOnly = true;
    undo.push(() => { control.readOnly = false; });
    } else if (!control.inert) {
    if (activeIn(control) === control && canFocus(submit)) submit.focus({ preventScroll: true });
    control.inert = true;
    undo.push(() => { control.inert = false; });
    }
    }
    return () => {
    for (const step of undo) step();
    };
    }
    function wireForm(dialog) {
    dialog.addEventListener('submit', async ( event) => {
    const box = (event.target);
    if (!box?.matches?.('form.ds-dialog__box') || box.closest('.ds-dialog') !== dialog || event.defaultPrevented) return;
    const setup = forms.get(dialog);
    if (!setup && !box.hasAttribute('data-ds-dialog-form')) return;
    event.preventDefault();
    if (dialog.hasAttribute('data-ds-busy') || overlays.get(dialog)?.phase !== 'open') return;
    const data = new FormData(box, (event).submitter);
    if (!emit(dialog, 'submit', { form: box, data }, { cancelable: true })) return;
    if (typeof setup?.onSubmit === 'function') {
    const submit = (event).submitter ?? box.querySelector('[type="submit"]');
    const release = holdForm(dialog, box, submit);
    try {
    await (submit ? busy(submit, () => setup.onSubmit(data, box), { done: false }) : setup.onSubmit(data, box));
    } catch {
    release();
    if (!dialog.isConnected) return;
    const speaker = part('toast');
    const region = speaker?.regionOfTheme(dialog.ownerDocument, dialog.closest('.ds-theme'), dialog.parentNode);
    if (region) speaker.toast(region, setup.errorText ?? t('actionFailed'), { kind: 'error' });
    if (canFocus(submit)) submit.focus({ preventScroll: true });
    return;
    }
    release();
    }
    close(dialog, { reason: 'submit' });
    });
    }
    function create(element) {
    return controllerOf(element);
    }
    const MODELESS = '.ds-dialog__box--modeless';
    function modelessOpened(box) {
    for (const body of box.querySelectorAll(':scope > .ds-dialog__body')) {
    const handle = watchScrolled(body);
    if (!body.querySelector(':scope > [data-ds-sentinel]')) handle.update();
    }
    const active = activeIn(box);
    if (active && box.contains(active)) return;
    const target = tabbables(box)[0];
    if (target) target.focus({ preventScroll: true });
    else {
    if (!box.hasAttribute('tabindex')) box.setAttribute('tabindex', '-1');
    box.focus({ preventScroll: true });
    }
    }
    define('dialog', {
    attribute: 'data-ds-dialog',
    init: (element) => create(element),
    global(root) {
    const onOpen = ( event) => {
    const box = (event.target);
    if (box?.matches?.(MODELESS)) modelessOpened(box);
    };
    root.addEventListener('ds:open', onOpen);
    return () => root.removeEventListener('ds:open', onOpen);
    },
    });
    internals.use('dialog', {
    confirm,
    alert,
    prompt,
    wire(dialog) {
    wireDialogList(dialog);
    wireForm(dialog);
    },
    });

    return Object.freeze({
      open: open,
      close: close,
      confirm: confirm,
      alert: alert,
      prompt: prompt,
      form: form,
      create: create
    });
  });

  add('form', ['count', 'counter'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const autosize = DS.dom.autosize, own = DS.dom.own, disown = DS.dom.disown;
    const announce = DS.a11y.announce;
    const number = DS.format.number, plural = DS.format.plural;
    function count(value, max) {
    const length = String(value ?? '').length;
    const limit = Number.isFinite(Number(max)) && Number(max) >= 0 && max !== null && max !== '' ? Math.floor(Number(max)) : null;
    if (limit === null) return { length, max: null, remaining: null, over: false };
    return { length, max: limit, remaining: limit - length, over: length > limit };
    }
    function limitOf(field) {
    const hard = field.getAttribute?.('maxlength');
    if (hard !== null && hard !== undefined && hard !== '' && Number(hard) >= 0) return Number(hard);
    const soft = field.getAttribute?.('data-ds-counter-max');
    return soft !== null && soft !== undefined && soft !== '' && Number(soft) >= 0 ? Number(soft) : null;
    }
    function warnOf(field, max) {
    const given = Number(field.getAttribute?.('data-ds-counter-warn'));
    if (Number.isFinite(given) && given >= 0 && field.hasAttribute?.('data-ds-counter-warn')) return given;
    return Math.max(5, Math.round(max / 10));
    }
    function counterController(field, signal) {
    const doc = field.ownerDocument;
    const named = field.getAttribute?.('data-ds-counter');
    const output = ((named && doc?.getElementById?.(named)) || field.closest?.('.ds-field')?.querySelector('.ds-field__counter') || null);
    const listeners = new Set();
    let last = (null);
    let zone = 'calm';
    if (output) own(output, 'classes', 'ds-field__counter--over');
    const state = () => count(field.value, limitOf(field));
    const paint = (speak) => {
    const now = state();
    if (output) {
    const text = now.max === null ? number(now.length) : `${number(now.length)} / ${number(now.max)}`;
    if (output.textContent !== text) output.textContent = text;
    output.classList.toggle('ds-field__counter--over', now.over);
    }
    const next = now.max === null ? 'calm' : now.over ? 'over' : now.remaining <= warnOf(field, now.max) ? 'near' : 'calm';
    if (speak && next !== zone && next !== 'calm') {
    const amount = next === 'over' ? now.length - (now.max) : (now.remaining);
    announce(next === 'over'
    ? plural(amount, { one: t('charactersOverOne'), other: t('charactersOver') })
    : plural(amount, { one: t('charactersLeftOne'), other: t('charactersLeft') }));
    }
    zone = next;
    if (last && last.length === now.length && last.max === now.max) return;
    const changed = last !== null;
    last = now;
    if (!changed) return;
    emit(field, 'change', now);
    listeners.forEach((fn) => fn(now));
    };
    field.addEventListener?.('input', () => paint(true), { signal });
    field.form?.addEventListener('reset', () => setTimeout(() => paint(false)), { signal });
    doc?.addEventListener?.('ds:config', () => paint(false), { signal });
    paint(false);
    return {
    get() { return state(); },
    set(value) {
    field.value = String(value ?? '');
    paint(false);
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    update() { paint(false); },
    destroy() {
    listeners.clear();
    if (output) disown(output, 'classes', 'ds-field__counter--over');
    },
    };
    }
    define('autosize', {
    selector: 'textarea.ds-input--grow',
    init(el) {
    const handle = autosize( (el));
    return { update: () => handle.update(), destroy: () => handle.destroy() };
    },
    });
    define('counter', {
    attribute: 'data-ds-counter',
    attributes: ['maxlength', 'data-ds-counter-max', 'data-ds-counter-warn'],
    init(el, ctx) {
    return counterController(el, ctx.signal);
    },
    });
    function counter(el) {
    const abort = typeof AbortController === 'function' ? new AbortController() : null;
    const made = counterController(el, abort?.signal ?? null);
    const destroy = made.destroy;
    made.destroy = () => {
    destroy();
    abort?.abort();
    };
    return made;
    }

    return Object.freeze({
      count: count,
      counter: counter
    });
  });

  add('formatted', ['text', 'exact', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit;
    const own = DS.dom.own, disown = DS.dom.disown;
    const number = DS.format.number, percent = DS.format.percent, currency = DS.format.currency, bytes = DS.format.bytes, duration = DS.format.duration;
    const KINDS = Object.freeze(['number', 'compact', 'percent', 'currency', 'bytes', 'duration']);
    const OPTION_ATTRIBUTES = Object.freeze(['currency', 'digits', 'base', 'style', 'signed', 'tip']);
    const EXACT_DIGITS = 6;
    function parse(value) {
    if (typeof value === 'number') return Number.isFinite(value) ? value : null;
    if (typeof value !== 'string') return null;
    const trimmed = value.trim();
    if (!trimmed) return null;
    const parsed = Number(trimmed);
    return Number.isFinite(parsed) ? parsed : null;
    }
    function numberOptions(options) {
    const out = {};
    if (options.locale) out.locale = options.locale;
    if (options.signed) out.signDisplay = 'exceptZero';
    const digits = Number.parseInt(String(options.digits ?? ''), 10);
    if (Number.isInteger(digits) && digits >= 0 && digits <= 20) {
    out.maximumFractionDigits = digits;
    if (options.kind === 'currency') out.minimumFractionDigits = Math.min(digits, 2);
    }
    return out;
    }
    function text(value, options = {}) {
    const raw = parse(value);
    if (raw === null) return '';
    const kind = KINDS.includes(options.kind ?? '') ? options.kind : 'number';
    const settings = numberOptions({ ...options, kind });
    switch (kind) {
    case 'compact':
    return number(raw, { ...settings, notation: 'compact', compactDisplay: options.style === 'long' ? 'long' : 'short' });
    case 'percent':
    return percent(raw, { maximumFractionDigits: 1, ...settings });
    case 'currency':
    try {
    return currency(raw, String(options.currency || 'USD').toUpperCase(), settings);
    } catch {
    return number(raw, settings);
    }
    case 'bytes':
    return bytes(raw, { ...settings, base: Number(options.base) === 1024 ? 1024 : 1000 });
    case 'duration': {
    const style = ['short', 'long', 'narrow', 'clock'].includes(options.style ?? '') ? options.style : 'short';
    return duration(raw, { locale: options.locale, style });
    }
    default:
    return number(raw, settings);
    }
    }
    function exact(value, options = {}) {
    const raw = parse(value);
    if (raw === null) return '';
    const kind = KINDS.includes(options.kind ?? '') ? options.kind : 'number';
    const locale = { ...(options.locale ? { locale: options.locale } : {}), ...(options.signed ? { signDisplay: 'exceptZero' } : {}) };
    let full;
    if (kind === 'bytes') full = number(raw, { ...locale, signDisplay: 'auto', style: 'unit', unit: 'byte', unitDisplay: 'long', maximumFractionDigits: 0 });
    else if (kind === 'duration') full = duration(raw, { locale: options.locale, style: 'long', parts: 5 });
    else if (kind === 'percent') full = percent(raw, { ...locale, maximumFractionDigits: EXACT_DIGITS });
    else if (kind === 'currency') {
    try {
    full = currency(raw, String(options.currency || 'USD').toUpperCase(), { ...locale, maximumFractionDigits: Math.max(2, EXACT_DIGITS) });
    } catch {
    full = number(raw, { ...locale, maximumFractionDigits: EXACT_DIGITS });
    }
    } else full = number(raw, { ...locale, maximumFractionDigits: EXACT_DIGITS });
    return full === text(raw, options) ? '' : full;
    }
    function controller(el, given, signal) {
    const listeners = new Set();
    let live = true;
    let written = '';
    let wroteTip = false;
    let value = null;
    const setting = (name) => {
    if (given[name] !== undefined) return given[name];
    const attribute = `data-ds-format-${name}`;
    if (!el.hasAttribute(attribute)) return undefined;
    const read = el.getAttribute(attribute);
    return name === 'signed' ? read !== 'false' : read;
    };
    const settings = () => {
    const out = { kind: given.kind ?? el.getAttribute('data-ds-format') ?? 'number' };
    for (const name of OPTION_ATTRIBUTES) {
    const read = setting(name);
    if (read !== undefined && name !== 'tip') out[name] = read;
    }
    if (given.locale) out.locale = given.locale;
    return out;
    };
    const source = () => (el.hasAttribute('data-ds-value') ? el.getAttribute('data-ds-value') : value);
    const writeTip = (tip) => {
    const off = given.tip === false || setting('tip') === 'off';
    if (off || !tip) {
    if (wroteTip) {
    wroteTip = false;
    el.removeAttribute('data-tip');
    disown(el, 'attrs', 'data-tip');
    }
    return;
    }
    if (!wroteTip) {
    if (el.hasAttribute('data-tip')) return;
    wroteTip = true;
    own(el, 'attrs', 'data-tip');
    }
    if (el.getAttribute('data-tip') !== tip) el.setAttribute('data-tip', tip);
    };
    const render = () => {
    const current = settings();
    const raw = source();
    const next = text(raw, current);
    writeTip(exact(raw, current));
    if (parse(raw) === null) return false;
    if (el.textContent === next) {
    written = next;
    return false;
    }
    if (el.childNodes.length === 1 && el.firstChild.nodeType === 3) el.firstChild.nodeValue = next;
    else el.textContent = next;
    written = next;
    watcher?.takeRecords();
    for (const fn of [...listeners]) fn({ value: parse(raw), text: next });
    return true;
    };
    value = given.value !== undefined ? given.value : el.textContent;
    const View = el.ownerDocument?.defaultView ?? globalThis;
    const watcher = typeof View.MutationObserver === 'function' ? new View.MutationObserver(() => {
    if (!live) return;
    const shown = el.textContent;
    if (shown !== written && !el.hasAttribute('data-ds-value')) value = shown;
    render();
    }) : null;
    watcher?.observe(el, { childList: true, characterData: true, subtree: true });
    const relocale = ( event) => {
    if (event.detail?.changed?.includes('locale')) render();
    };
    el.ownerDocument?.addEventListener?.('ds:config', relocale, signal ? { signal } : undefined);
    render();
    return {
    get() {
    return { value: parse(source()), text: el.textContent ?? '' };
    },
    set(next) {
    if (el.hasAttribute('data-ds-value')) el.setAttribute('data-ds-value', next === null || next === undefined ? '' : String(next));
    value = next;
    if (render() && live) emit(el, 'change', { value: parse(next), text: written });
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    update() {
    if (live) render();
    },
    destroy() {
    if (!live) return;
    live = false;
    watcher?.disconnect();
    el.ownerDocument?.removeEventListener?.('ds:config', relocale);
    listeners.clear();
    if (wroteTip) {
    wroteTip = false;
    el.removeAttribute('data-tip');
    disown(el, 'attrs', 'data-tip');
    }
    },
    };
    }
    define('formatted', {
    attribute: 'data-ds-format',
    attributes: ['data-ds-value', ...OPTION_ATTRIBUTES.map((name) => `data-ds-format-${name}`)],
    init(el, ctx) {
    return controller(el, {}, ctx.signal);
    },
    });
    function create(el, options = {}) {
    return controller(el, options, null);
    }

    return Object.freeze({
      text: text,
      exact: exact,
      create: create
    });
  });

  add('highlight', ['KINDS', 'register', 'language', 'languages', 'tokenize', 'use', 'highlight', 'lines', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, runtime = DS.core.runtime;
    const morphHtml = DS.dom.morphHtml;
    const shared = runtime.state('highlight', () => ({ languages: new Map(), aliases: new Map(), external: null }));
    const KINDS = Object.freeze(['keyword', 'string', 'number', 'comment', 'function', 'type', 'property', 'tag', 'attribute', 'variable', 'constant', 'regex', 'operator', 'punctuation', 'meta', 'inserted', 'deleted']);
    const words = (text) => new Set(text.split(/\s+/).filter(Boolean));
    function compile(rules) {
    let groups = 1;
    const entries = rules.map(([kind, pattern]) => {
    const entry = { kind, group: groups };
    groups += 1 + new RegExp(`${pattern.source}|`).exec('').length - 1;
    return entry;
    });
    const flags = new Set(['g', 'm']);
    for (const [, pattern] of rules) if (pattern.flags.includes('i')) flags.add('i');
    return { entries, regex: new RegExp(rules.map(([, pattern]) => `(${pattern.source})`).join('|'), [...flags].join('')) };
    }
    function find(name) {
    const key = String(name ?? '').trim().toLowerCase().replace(/^language-/, '');
    const id = shared.aliases.get(key) ?? key;
    return shared.languages.get(id) ?? null;
    }
    function run(lang, code) {
    const out = [];
    const state = { prev: null, lang, code, data: {} };
    const push = (kind, text) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && last[0] === kind) last[1] += text;
    else out.push([kind, text]);
    if (kind === 'comment') return;
    const trimmed = text.trim();
    if (!trimmed) return;
    if (state.prev) {
    state.prev.kind = kind;
    state.prev.text = trimmed;
    } else state.prev = { kind, text: trimmed };
    };
    if (typeof lang.tokenize === 'function') {
    for (const token of lang.tokenize(code, state)) push(token[0] ?? null, token[1]);
    return out;
    }
    const { regex, entries } = lang.compiled;
    let pos = 0;
    while (pos < code.length) {
    regex.lastIndex = pos;
    const match = regex.exec(code);
    if (!match) {
    push(null, code.slice(pos));
    break;
    }
    if (match.index > pos) push(null, code.slice(pos, match.index));
    if (!match[0]) {
    push(null, code[match.index]);
    pos = match.index + 1;
    continue;
    }
    const entry = entries.find((candidate) => match[candidate.group] !== undefined);
    const end = match.index + match[0].length;
    let result = entry.kind;
    if (typeof result === 'function') result = result(match[0], state, match.index, end);
    if (Array.isArray(result)) {
    let length = 0;
    for (const [kind, text] of result) {
    push(kind ?? null, text);
    length += text.length;
    }
    pos = match.index + Math.max(1, length);
    } else {
    push(result ?? null, match[0]);
    pos = end;
    }
    }
    return out;
    }
    function at(pattern, code, index) {
    pattern.lastIndex = index;
    return pattern.test(code);
    }
    const NEXT_CALL = /[ \t]*(?:<[\w$<>, \t[\]]*>)?\(/y;
    const NEXT_COLON = /[ \t]*:(?!:)/y;
    const NEXT_WORD = /[ \t]+[A-Za-z_$]/y;
    function lineStart(state, index) {
    const data = state.data;
    if (data.lineAt === undefined || index < data.lineAt) {
    data.lineAt = state.code.lastIndexOf('\n', index - 1) + 1;
    data.line = 'start';
    }
    const code = state.code;
    for (let at = data.lineAt; at < index; at += 1) {
    const char = code[at];
    const blank = char === ' ' || char === '\t';
    if (char === '\n') data.line = 'start';
    else if (data.line === 'start') data.line = blank ? 'start' : char === '$' ? 'dollar' : 'other';
    else if (data.line === 'dollar' || data.line === 'prompt') data.line = blank ? 'prompt' : 'other';
    }
    data.lineAt = index;
    return data.line === 'start' || data.line === 'prompt';
    }
    const BLOCK_COMMENT = /\/\*[\s\S]*?(?:\*\/|$(?![\s\S]))/;
    const LINE_COMMENT = /\/\/[^\n]*/;
    const DOUBLE = /"(?:[^"\\\n]|\\[\s\S])*"?/;
    const SINGLE = /'(?:[^'\\\n]|\\[\s\S])*'?/;
    const C_NUMBER = /(?<![\w$.])(?:0[xX][\da-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|(?:\d[\d_]*(?:\.[\d_]*)?|\.\d[\d_]*)(?:[eE][+-]?\d[\d_]*)?)[nmuflUL]*(?![\w$])/;
    const IDENT = /[A-Za-z_$][\w$]*/;
    const JS_KEYWORDS = words(`break case catch class const continue debugger default delete do else export extends finally for
      function if import in instanceof let new return static switch throw try typeof var void while with yield async await
      of from as get set`);
    const TS_KEYWORDS = words(`abstract declare enum implements interface keyof namespace private protected public readonly
      infer is satisfies module override asserts unique accessor`);
    const JS_CONSTANTS = words('true false null undefined NaN Infinity this super');
    const TS_TYPES = words('string number boolean any unknown never object symbol bigint');
    const REGEX_AFTER = /^(?:[(,=:[!&|?{};+\-*%<>~^]|=>|&&|\|\||\?\?|return|typeof|instanceof|in|of|new|delete|void|throw|case|do|else|yield|await)$/;
    function jsLike(typescript) {
    const keywords = typescript ? new Set([...JS_KEYWORDS, ...TS_KEYWORDS]) : JS_KEYWORDS;
    const identifier = (word, state, index, end) => {
    const prev = state.prev?.text;
    const code = state.code;
    if (prev === '.' || prev === '?.') return at(NEXT_CALL, code, end) ? 'function' : 'property';
    if (word === 'type' && typescript) return at(NEXT_WORD, code, end) ? 'keyword' : null;
    if (keywords.has(word)) return ['get', 'set', 'of', 'from', 'as', 'async'].includes(word) && at(NEXT_COLON, code, end) ? 'property' : 'keyword';
    if (JS_CONSTANTS.has(word)) return 'constant';
    if (at(NEXT_CALL, code, end)) return prev === 'new' ? 'type' : 'function';
    if (prev === 'function') return 'function';
    if (prev === 'class' || prev === 'extends' || prev === 'implements' || prev === 'interface' || prev === 'enum' || prev === 'new') return 'type';
    if (typescript && TS_TYPES.has(word)) return 'type';
    if (/^[A-Z][A-Z\d_]+$/.test(word)) return 'constant';
    if (/^[A-Z]/.test(word)) return 'type';
    if ((prev === '{' || prev === ',') && at(NEXT_COLON, code, end)) return 'property';
    return null;
    };
    const regexLiteral = (text, state) => {
    const prev = state.prev;
    if (!prev || REGEX_AFTER.test(prev.text) || (prev.kind === 'keyword' && prev.text !== 'this')) return 'regex';
    return [['operator', '/']];
    };
    return [
    ['comment', BLOCK_COMMENT],
    ['comment', LINE_COMMENT],
    ['string', /`(?:[^`\\]|\\[\s\S])*`?/],
    ['string', DOUBLE],
    ['string', SINGLE],
    [regexLiteral, /\/(?![*/])(?:[^/\\[\n]|\\.|\[(?:[^\]\\\n]|\\.)*\])+\/[dgimsuyv]*/],
    ['meta', /@[A-Za-z_$][\w$.]*/],
    ['number', C_NUMBER],
    [identifier, IDENT],
    ['operator', /=>|\?\.|\?\?=?|\.\.\.|[-+*%&|^!<>=~?]+=?|\//],
    ['punctuation', /[{}[\]();,.:]/],
    ];
    }
    const JSON_RULES = [
    ['comment', BLOCK_COMMENT],
    ['comment', LINE_COMMENT],
    ['property', /"(?:[^"\\\n]|\\.)*"(?=\s*:)/],
    ['string', DOUBLE],
    ['number', /-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?(?![\w.])/],
    ['constant', /\b(?:true|false|null)\b/],
    ['punctuation', /[{}[\],:]/],
    ];
    const SH_KEYWORDS = words(`if then else elif fi for while until do done case esac in function return select time export
      local readonly declare typeset unset shift break continue exit source alias unalias set trap eval exec`);
    const SH_COMMAND_AFTER = /^(?:\||\|\||&&|;|&|\$\(|\(|`|then|do|else|elif|if|while|until|!|sudo|exec|time|xargs)$/;
    const SH_RULES = [
    ['comment', /(?<=^|[\s;|&(])#[^\n]*/],
    ['meta', /^[ \t]*\$(?=[ \t])/],
    ['string', /\$?'[^']*'?/],
    ['string', DOUBLE],
    ['variable', /\$(?:\{[^}\n]*\}?|[A-Za-z_]\w*|[0-9@#?$!*-])/],
    ['operator', /\$\(\(?|\)\)?|&&|\|\||[|&;]|[<>]{1,2}&?\d?|=/],
    ['variable', /[A-Za-z_]\w*(?==)/],
    ['attribute', /(?<=[\s=])--?[A-Za-z][\w-]*/],
    ['number', /(?<![\w./:-])\d+(?:\.\d+)?(?![\w./:-])/],
    [(word, state, index) => {
    const command = lineStart(state, index) || SH_COMMAND_AFTER.test(state.prev?.text ?? '');
    if (SH_KEYWORDS.has(word) && command) return 'keyword';
    if (command) return 'function';
    return null;
    }, /[A-Za-z_.\/~][\w./~+-]*/],
    ['punctuation', /[(){}[\]]/],
    ];
    const HTML_TAG = /<\/?[A-Za-z][\w:.-]*(?:\s+[^\s"'>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*\s*\/?>?/;
    const TAG_PART = /(<\/?)([\w:.-]+)|(\s+)|([^\s"'>/=]+)(?:(\s*=\s*)("[^"]*"|'[^']*'|[^\s"'=<>`]+))?|(\/?>)/gy;
    function markupTag(embed) {
    return (text, state, index, end) => {
    const tokens = [];
    TAG_PART.lastIndex = 0;
    let name = '';
    for (let part = TAG_PART.exec(text); part && part[0]; part = TAG_PART.exec(text)) {
    if (part[1]) {
    tokens.push(['punctuation', part[1]], ['tag', part[2]]);
    name = part[1] === '<' ? part[2].toLowerCase() : '';
    } else if (part[3]) tokens.push([null, part[3]]);
    else if (part[4]) {
    tokens.push(['attribute', part[4]]);
    if (part[5]) tokens.push(['punctuation', part[5]], ['string', part[6]]);
    } else tokens.push(['punctuation', part[7]]);
    }
    const consumed = tokens.reduce((sum, token) => sum + token[1].length, 0);
    if (consumed < text.length) tokens.push([null, text.slice(consumed)]);
    const inner = embed && !text.endsWith('/>') ? { script: 'js', style: 'css' }[name] : null;
    if (inner) {
    const close = state.code.toLowerCase().indexOf(`</${name}`, end);
    const body = state.code.slice(end, close < 0 ? state.code.length : close);
    if (body) tokens.push(...run(find(inner), body));
    }
    return tokens;
    };
    }
    function markupRules(embed) {
    return [
    ['comment', /<!--[\s\S]*?(?:-->|$(?![\s\S]))/],
    ['string', /<!\[CDATA\[[\s\S]*?(?:\]\]>|$(?![\s\S]))/],
    ['meta', /<![A-Za-z][^>]*>?|<\?[\s\S]*?(?:\?>|$(?![\s\S]))/],
    [markupTag(embed), HTML_TAG],
    ['constant', /&(?:#\d+|#[xX][\da-fA-F]+|[A-Za-z][\w]*);/],
    ];
    }
    const RULE_BLOCKS = /^(?:media|supports|layer|container|document|scope|starting-style)$/;
    function cssTokens(code) {
    const tokens = [];
    const stack = [];
    let value = false;
    const RULE = /(\/\*[\s\S]*?(?:\*\/|$(?![\s\S])))|("(?:[^"\\\n]|\\[\s\S])*"?|'(?:[^'\\\n]|\\[\s\S])*'?)|(@[\w-]+)|(!important\b)|(#[\w-]+)|(--[\w-]+)|((?:\d+\.?\d*|\.\d+)(?:%|[A-Za-z]+)?)|(::?[\w-]+)|([A-Za-z_-][\w-]*)|([{}();,:>+~*=[\]]|\.(?=[A-Za-z_-]))|(\s+)/gy;
    const declaring = () => stack.length && stack[stack.length - 1] === 'block' && !value;
    const isProperty = (end) => {
    const rest = /^[ \t]*:([^;{}]*)([;{}]|$)/.exec(code.slice(end, end + 400));
    return Boolean(rest) && rest[2] !== '{';
    };
    let pos = 0;
    let classNext = false;
    let atRule = '';
    while (pos < code.length) {
    RULE.lastIndex = pos;
    const m = RULE.exec(code);
    if (!m) {
    tokens.push([null, code[pos]]);
    pos += 1;
    continue;
    }
    const text = m[0];
    const end = pos + text.length;
    if (m[1]) tokens.push(['comment', text]);
    else if (m[2]) tokens.push(['string', text]);
    else if (m[3]) {
    tokens.push(['keyword', text]);
    stack.push('at');
    atRule = text.slice(1).toLowerCase();
    } else if (m[4]) tokens.push(['keyword', text]);
    else if (m[5]) tokens.push([value ? 'number' : 'type', text]);
    else if (m[6]) tokens.push([declaring() && isProperty(end) ? 'property' : 'variable', text]);
    else if (m[7]) tokens.push(['number', text]);
    else if (m[8]) {
    if (declaring() || value) {
    tokens.push(['punctuation', ':']);
    if (declaring()) value = true;
    classNext = false;
    pos += 1;
    continue;
    }
    tokens.push(['keyword', text]);
    }
    else if (m[9]) {
    let kind;
    if (classNext) kind = 'type';
    else if (code[end] === '(') kind = 'function';
    else if (value) kind = 'constant';
    else if (declaring() && isProperty(end)) kind = 'property';
    else if (stack[stack.length - 1] === 'at') kind = code[end] === ':' ? 'property' : null;
    else kind = 'tag';
    tokens.push([kind, text]);
    } else if (m[10]) {
    if (text === '{') {
    const nested = stack[stack.length - 1] === 'at' && RULE_BLOCKS.test(atRule);
    if (stack[stack.length - 1] === 'at') stack.pop();
    stack.push(nested ? 'rules' : 'block');
    atRule = '';
    value = false;
    } else if (text === '}') {
    stack.pop();
    value = false;
    } else if (text === ';') {
    if (stack[stack.length - 1] === 'at') stack.pop();
    atRule = '';
    value = false;
    } else if (text === ':' && declaring()) value = true;
    tokens.push(['punctuation', text]);
    classNext = text === '.' && !value;
    pos = end;
    continue;
    } else tokens.push([null, text]);
    classNext = false;
    pos = end;
    }
    return tokens;
    }
    const SQL_KEYWORDS = words(`select from where and or not in is like ilike between exists as on join inner left right full outer
      cross natural using group by order having limit offset fetch first next rows only union all intersect except distinct
      insert into values update set delete create table view index unique primary key foreign references constraint default
      check drop alter add column rename to if replace temporary temp with recursive returning case when then else end
      begin commit rollback transaction grant revoke cascade asc desc nulls over partition window lateral do nothing conflict
      truncate explain analyze materialized trigger function procedure returns language declare schema database sequence
      collate filter within`);
    const SQL_TYPES = words(`int integer bigint smallint tinyint decimal numeric real float double precision char varchar
      nvarchar text date time timestamp timestamptz interval boolean bool uuid json jsonb blob bytea serial bigserial money
      inet cidr array`);
    const SQL_CONSTANTS = words('true false null unknown current_date current_time current_timestamp');
    const SQL_RULES = [
    ['comment', /--[^\n]*/],
    ['comment', BLOCK_COMMENT],
    ['string', /[eEnN]?'(?:[^']|'')*'?/],
    ['property', /"(?:[^"]|"")*"?|`[^`\n]*`?|\[[^\]\n]*\]/],
    ['variable', /[@:$][A-Za-z_]\w*|\$\d+|\?/],
    ['number', /(?<![\w.])(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?(?![\w])/],
    [(word, state, index, end) => {
    const lower = word.toLowerCase();
    if (state.prev?.text === '.') return 'property';
    if (at(NEXT_CALL, state.code, end) && !SQL_KEYWORDS.has(lower)) return 'function';
    if (SQL_KEYWORDS.has(lower)) return 'keyword';
    if (SQL_TYPES.has(lower)) return 'type';
    if (SQL_CONSTANTS.has(lower)) return 'constant';
    return null;
    }, /[A-Za-z_][\w$]*/],
    ['operator', /<>|!=|<=|>=|\|\||::|[-+*/%<>=~!|&^]/],
    ['punctuation', /[(),;.[\]]/],
    ];
    const YAML_SCALAR_START = '(?<=(?:^[ \\t]*(?:-[ \\t]+)*|(?:^|[ \\t])-[ \\t]+|:[ \\t]+|[[,{][ \\t]*))';
    const YAML_SCALAR_END = '(?=[ \\t]*(?:#|$|,[ \\t]|\\]|\\}))';
    const yamlItems = (text) => text.split(/(-)/).filter(Boolean).map((part) => [part === '-' ? 'punctuation' : null, part]);
    const YAML_RULES = [
    ['comment', /(?<=^|[ \t])#[^\n]*/],
    ['meta', /^(?:---|\.\.\.)(?=[ \t]|$)/],
    [yamlItems, /^[ \t]*-(?:[ \t]+-)*(?=[ \t]|$)/],
    ['property', /(?<=^[ \t]*(?:-[ \t]+)*)(?:"(?:[^"\\\n]|\\.)*"|'(?:[^'\n]|'')*'|[^\s#'"{}[\],:&*!|>%@`-](?:[^\n:#]|:(?=\S)|(?<=\S)#)*?|-[^\s#:][^\n:#]*?)(?=[ \t]*:(?:[ \t]|$))/],
    ['property', /(?<=[{,][ \t]*)(?:"(?:[^"\\\n]|\\.)*"|'(?:[^'\n]|'')*'|[^\s#'"{}[\],:&*!|>%@`-](?:[^\n:#,[\]{}]|:(?=[^\s,[\]{}])|(?<=\S)#)*?|-[^\s#:,[\]{}][^\n:#,[\]{}]*?)(?=[ \t]*:(?:[ \t]|$))/],
    ['string', /"(?:[^"\\\n]|\\[\s\S])*"?|'(?:[^'\n]|'')*'?/],
    ['meta', /[&*][\w-]+/],
    ['type', /!!?[\w-]*/],
    ['punctuation', /(?<=^[ \t]*(?:-[ \t]+)*)-(?=[ \t]|$)|(?<=:[ \t]+)[|>][+-]?\d*(?=[ \t]*(?:#|$))|[{}[\],]|:(?=[ \t]|$)/],
    ['constant', new RegExp(`${YAML_SCALAR_START}(?:true|false|null|yes|no|on|off|~)${YAML_SCALAR_END}`, 'i')],
    ['number', new RegExp(`${YAML_SCALAR_START}[-+]?(?:0x[\\da-fA-F]+|0o[0-7]+|\\d[\\d_]*(?:\\.\\d*)?(?:[eE][+-]?\\d+)?|\\.inf|\\.nan)${YAML_SCALAR_END}`, 'i')],
    [null, /[ \t]+(?:-[ \t]+)*/],
    ];
    const GO_KEYWORDS = words(`break case chan const continue default defer else fallthrough for func go goto if import interface
      map package range return select struct switch type var`);
    const GO_TYPES = words(`bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint
      uint8 uint16 uint32 uint64 uintptr any comparable`);
    const GO_CONSTANTS = words('true false nil iota');
    const GO_RULES = [
    ['comment', BLOCK_COMMENT],
    ['comment', LINE_COMMENT],
    ['string', /`[^`]*`?/],
    ['string', DOUBLE],
    ['string', /'(?:[^'\\\n]|\\.[^'\n]*)'?/],
    ['number', /(?<![\w.])(?:0[xX][\da-fA-F_]+|0[bB][01_]+|0[oO]?[0-7_]+|(?:\d[\d_]*(?:\.[\d_]*)?|\.\d[\d_]*)(?:[eE][+-]?\d[\d_]*)?)i?(?![\w])/],
    [(word, state, index, end) => {
    const prev = state.prev?.text;
    if (GO_KEYWORDS.has(word)) return 'keyword';
    if (GO_CONSTANTS.has(word)) return 'constant';
    if (GO_TYPES.has(word)) return 'type';
    if (at(NEXT_CALL, state.code, end)) return 'function';
    if (prev === 'func') return 'function';
    if (prev === 'type' || prev === 'struct') return 'type';
    if (prev === '.') return /^[A-Z]/.test(word) ? 'type' : 'property';
    return null;
    }, /[A-Za-z_]\w*/],
    ['operator', /:=|<-|&\^=?|\.\.\.|[-+*/%&|^!<>=]+/],
    ['punctuation', /[{}[\]();,.:]/],
    ];
    const PY_KEYWORDS = words(`and as assert async await break class continue def del elif else except finally for from global
      if import in is lambda nonlocal not or pass raise return try while with yield match case`);
    const PY_CONSTANTS = words('__name__ __file__ True False None NotImplemented Ellipsis');
    const PY_BUILTINS = words(`int str float bool list dict set tuple bytes object type range len print open enumerate zip map
      filter sorted sum min max abs any all super isinstance issubclass getattr setattr hasattr iter next repr round`);
    const PY_RULES = [
    ['comment', /#[^\n]*/],
    ['string', /(?<![\w])[rRbBuUfF]{0,2}(?:"""[\s\S]*?(?:"""|$(?![\s\S]))|'''[\s\S]*?(?:'''|$(?![\s\S]))|"(?:[^"\\\n]|\\[\s\S])*"?|'(?:[^'\\\n]|\\[\s\S])*'?)/],
    ['meta', /(?<=^[ \t]*)@[\w.]+/],
    ['number', /(?<![\w.])(?:0[xX][\da-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|(?:\d[\d_]*(?:\.[\d_]*)?|\.\d[\d_]*)(?:[eE][+-]?\d[\d_]*)?[jJ]?)(?![\w])/],
    [(word, state, index, end) => {
    const prev = state.prev?.text;
    if (prev === '.') return at(NEXT_CALL, state.code, end) ? 'function' : 'property';
    if (PY_KEYWORDS.has(word)) return 'keyword';
    if (PY_CONSTANTS.has(word)) return 'constant';
    if (word === 'self' || word === 'cls') return 'variable';
    if (prev === 'def') return 'function';
    if (prev === 'class') return 'type';
    if (at(NEXT_CALL, state.code, end)) return /^[A-Z]/.test(word) ? 'type' : 'function';
    if (PY_BUILTINS.has(word)) return 'type';
    if (/^[A-Z][A-Z\d_]+$/.test(word)) return 'constant';
    if (/^[A-Z]/.test(word)) return 'type';
    return null;
    }, /[A-Za-z_]\w*/],
    ['operator', /->|:=|\*\*=?|\/\/=?|[-+*/%@&|^~<>=!]=?/],
    ['punctuation', /[{}[\]();,.:]/],
    ];
    const DIFF_RULES = [
    ['meta', /^(?:diff|index|similarity|dissimilarity|rename|copy|new file|deleted file|old mode|new mode|Binary files)\b[^\n]*/],
    ['meta', /^(?:\+\+\+|---)(?:[ \t][^\n]*)?$/],
    ['meta', /^@@[^\n]*/],
    ['inserted', /^[+>][^\n]*/],
    ['deleted', /^[-<][^\n]*/],
    ['comment', /^\\[^\n]*/],
    ];
    function register(name, rules, aliases = []) {
    const id = String(name).toLowerCase();
    const lang = typeof rules === 'function' ? { id, tokenize: rules } : { id, compiled: compile(rules) };
    shared.languages.set(id, lang);
    for (const alias of aliases) shared.aliases.set(String(alias).toLowerCase(), id);
    }
    function builtIn() {
    if (shared.languages.has('json')) return;
    register('json', JSON_RULES, ['jsonc', 'json5', 'webmanifest']);
    register('js', jsLike(false), ['javascript', 'mjs', 'cjs', 'jsx', 'node']);
    register('ts', jsLike(true), ['typescript', 'tsx', 'mts', 'cts']);
    register('sh', SH_RULES, ['bash', 'shell', 'zsh', 'console', 'shellsession', 'sh-session']);
    register('html', markupRules(true), ['htm', 'xhtml', 'vue', 'svelte']);
    register('xml', markupRules(false), ['svg', 'xsl', 'xslt', 'plist', 'rss', 'atom', 'xaml']);
    register('css', (code) => cssTokens(code), ['scss', 'less']);
    register('sql', SQL_RULES, ['psql', 'pgsql', 'postgres', 'postgresql', 'mysql', 'sqlite', 'plsql', 'tsql']);
    register('yaml', YAML_RULES, ['yml']);
    register('go', GO_RULES, ['golang']);
    register('py', PY_RULES, ['python', 'python3', 'py3', 'pyi']);
    register('diff', DIFF_RULES, ['patch', 'udiff']);
    }
    builtIn();
    function language(name) {
    return find(name)?.id ?? null;
    }
    function languages() {
    return [...shared.languages.keys()].sort();
    }
    function tokenize(code, lang) {
    const text = String(code ?? '');
    const found = find(lang);
    if (!found || !text) return text ? [{ kind: null, text }] : [];
    return run(found, text).map(([kind, part]) => ({ kind, text: part }));
    }
    const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' };
    const SPECIAL = /[&<>"]/;
    const escape = (text) => (SPECIAL.test(text) ? text.replace(/[&<>"]/g, (char) => ESCAPES[char]) : text);
    const span = (kind, text) => (kind ? `<span class="ds-highlight__${kind}">${escape(text)}</span>` : escape(text));
    function use(highlighter) {
    shared.external = typeof highlighter === 'function' ? highlighter : null;
    }
    function external(code, lang) {
    if (!shared.external || !lang) return null;
    const html = shared.external(code, String(lang));
    return typeof html === 'string' ? html : null;
    }
    function highlight(code, lang) {
    const text = String(code ?? '');
    const outside = external(text, lang);
    if (outside !== null) return outside;
    return tokenize(text, lang).map((token) => span(token.kind, token.text)).join('');
    }
    function splitMarkup(html) {
    const result = [];
    const open = [];
    let line = '';
    for (const [part] of html.matchAll(/<\/?[A-Za-z][^>]*>|<!--[\s\S]*?-->|[^<]+|</g)) {
    if (part[0] === '<' && part.length > 1 && part[1] !== '!') {
    if (part[1] === '/') open.pop();
    else if (!part.endsWith('/>') && !/^<(?:br|img|wbr|hr|input)\b/i.test(part)) open.push(part);
    line += part;
    continue;
    }
    const pieces = part.split('\n');
    line += pieces[0];
    for (const piece of pieces.slice(1)) {
    result.push(line + open.map((tag) => `</${/^<([A-Za-z][\w-]*)/.exec(tag)[1]}>`).reverse().join(''));
    line = open.join('') + piece;
    }
    }
    result.push(line);
    return result;
    }
    function lines(code, lang) {
    const text = String(code ?? '').replace(/\r\n?/g, '\n');
    const outside = external(text, lang);
    if (outside !== null) return splitMarkup(outside);
    const found = find(lang);
    if (!found || !text) return text.split('\n').map(escape);
    const result = [];
    let line = '';
    for (const [kind, part] of run(found, text)) {
    if (!part.includes('\n')) {
    line += span(kind, part);
    continue;
    }
    const pieces = part.split('\n');
    for (let index = 0; index < pieces.length; index += 1) {
    if (index) {
    result.push(line);
    line = '';
    }
    if (pieces[index]) line += span(kind, pieces[index]);
    }
    }
    result.push(line);
    return result;
    }
    function languageOf(el, fallback) {
    const named = el.getAttribute?.('data-ds-highlight');
    if (named) return named;
    const cls = /(?:^|\s)lang(?:uage)?-([\w+#.-]+)/.exec(el.className && typeof el.className === 'string' ? el.className : '');
    return cls ? cls[1] : fallback ?? '';
    }
    function controller(el, options, signal) {
    let code = options.code ?? el.textContent ?? '';
    let lang = options.language ?? languageOf(el);
    const listeners = new Set();
    const write = () => {
    if (lang && (find(lang) || shared.external)) morphHtml(el, highlight(code, lang));
    else if (el.childElementCount || el.textContent !== code) el.textContent = code;
    };
    write();
    return {
    get() { return code; },
    set(next) {
    const text = String(next ?? '');
    if (text === code) return;
    code = text;
    write();
    if (signal?.aborted) return;
    const detail = { code, language: language(lang) ?? lang };
    emit(el, 'change', detail);
    listeners.forEach((fn) => fn(detail));
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    update() {
    lang = languageOf(el, lang);
    write();
    },
    destroy() {
    listeners.clear();
    if (el.isConnected !== false && el.textContent === code) el.textContent = code;
    },
    };
    }
    define('highlight', {
    attribute: 'data-ds-highlight',
    init(el, ctx) {
    return controller(el, {}, ctx.signal);
    },
    });
    function create(el, options = {}) {
    return controller(el, options);
    }

    return Object.freeze({
      KINDS: KINDS,
      register: register,
      language: language,
      languages: languages,
      tokenize: tokenize,
      use: use,
      highlight: highlight,
      lines: lines,
      create: create
    });
  });

  add('code', ['parseLines', 'linesHtml', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, runtime = DS.core.runtime, t = DS.core.t;
    const disown = DS.dom.disown, morphHtml = DS.dom.morphHtml, own = DS.dom.own, roving = DS.dom.roving;
    const enter = DS.motion.enter, motionDuration = DS.motion.motionDuration;
    const makeIndicator = DS.indicator.create;
    const openStore = DS.store.open;
    const knownLanguage = DS.highlight.language, highlightLines = DS.highlight.lines;
    const shared = runtime.state('code', () => ({ ids: 0 }));
    const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' };
    const escape = (text) => text.replace(/[&<>"]/g, (char) => ESCAPES[char]);
    function parseLines(text) {
    const found = new Set();
    for (const part of String(text ?? '').split(/[,\s]+/)) {
    const range = /^(\d+)(?:-(\d+))?$/.exec(part);
    if (!range) continue;
    const from = Number(range[1]);
    const to = range[2] === undefined ? from : Number(range[2]);
    for (let line = Math.min(from, to); line <= Math.max(from, to) && line - Math.min(from, to) < 10000; line += 1) found.add(line);
    }
    return found;
    }
    function linesHtml(source, options = {}) {
    const text = String(source ?? '').replace(/\r\n?/g, '\n').replace(/\n$/, '');
    const lang = options.language ?? '';
    const rows = lang ? highlightLines(text, lang) : text.split('\n').map(escape);
    const raw = lang && knownLanguage(lang) === 'diff' ? text.split('\n') : null;
    const start = Number.isInteger(options.start) ? options.start : 1;
    const marks = options.marks ?? new Set();
    return rows.map((row, index) => {
    const number = start + index;
    const marked = marks.has(number);
    let cls = 'ds-code__line';
    if (marked) cls += ' ds-code__line--mark';
    if (raw && /^\+(?!\+\+)/.test(raw[index])) cls += ' ds-code__line--inserted';
    else if (raw && /^-(?!--)/.test(raw[index])) cls += ' ds-code__line--deleted';
    const tag = marked ? 'mark' : 'span';
    return `<${tag} class="${cls}" data-line="${number}">${row}${index < rows.length - 1 ? '\n' : ''}</${tag}>`;
    }).join('');
    }
    function blocksOf(root) {
    if (root.matches?.('pre')) return [root];
    return Array.from(root.querySelectorAll('pre.ds-code-block')).filter((pre) => pre.parentElement?.closest('[data-ds-code], .ds-code') === root);
    }
    function codeOf(pre) {
    let code = pre.querySelector(':scope > code');
    if (!code) {
    code = pre.ownerDocument.createElement('code');
    code.textContent = pre.textContent;
    pre.replaceChildren(code);
    }
    return code;
    }
    function languageOf(pre, code, root) {
    const named = pre.getAttribute('data-ds-code-lang') || code.getAttribute('data-ds-code-lang');
    if (named) return named;
    const cls = /(?:^|\s)lang(?:uage)?-([\w+#.-]+)/.exec(code.className || '');
    if (cls) return cls[1];
    return root.getAttribute('data-ds-code') || '';
    }
    function controller(root, options, signal) {
    const doc = root.ownerDocument;
    const listeners = new Map();
    const cleanups = [];
    const notify = (type, detail) => {
    if (signal?.aborted) return;
    emit(root, type, detail);
    listeners.get(type)?.forEach((fn) => fn(detail));
    };
    const listen = (target, type, fn) => {
    target.addEventListener(type, fn, signal ? { signal } : undefined);
    if (!signal) cleanups.push(() => target.removeEventListener(type, fn));
    };
    const claims = [];
    const claim = (el, kind, ...names) => {
    for (const name of names) {
    own(el, kind, name);
    claims.push([el, kind, name]);
    }
    };
    claim(root, 'attrs', 'data-state');
    claim(root, 'props', '--ds-code-max');
    const panels = blocksOf(root).map((pre) => {
    const code = codeOf(pre);
    if (!code.id) {
    shared.ids += 1;
    code.id = `ds-code-${shared.ids}`;
    claim(code, 'attrs', 'id');
    }
    claim(pre, 'attrs', 'aria-labelledby', 'hidden', 'id');
    claim(pre, 'props', '--ds-code-digits');
    claim(pre, 'classes', 'ds-code-block--wrap');
    return { pre, code, source: code.textContent ?? '' };
    });
    if (options.code !== undefined && panels[0]) panels[0].source = String(options.code);
    const sync = (panel) => {
    if (panel.text !== undefined && panel.code.textContent !== panel.text) panel.source = panel.code.textContent;
    };
    const tablist = root.querySelector('[role="tablist"]');
    const tabs = tablist ? Array.from(tablist.querySelectorAll('[role="tab"]')) : [];
    const tabOf = (panel) => tabs.find((tab) => tab.getAttribute('aria-controls') === panel.pre.id) ?? null;
    let index = Math.max(0, panels.findIndex((panel) => !panel.pre.hidden));
    const active = () => panels[index];
    const render = (panel) => {
    const lang = languageOf(panel.pre, panel.code, root);
    const start = Number.parseInt(panel.pre.getAttribute('data-ds-code-start') ?? root.getAttribute('data-ds-code-start') ?? '1', 10);
    const marks = parseLines(panel.pre.getAttribute('data-ds-code-mark') ?? (panels.length === 1 ? root.getAttribute('data-ds-code-mark') : null));
    const first = Number.isInteger(start) ? start : 1;
    morphHtml(panel.code, linesHtml(panel.source, { language: lang, start: first, marks }));
    panel.text = panel.code.textContent;
    const count = panel.code.childElementCount;
    panel.lines = count;
    panel.pre.style.setProperty('--ds-code-digits', String(String(first + Math.max(count, 1) - 1).length));
    panel.lang = lang;
    };
    const label = root.querySelector('.ds-code__lang');
    const copyButton = root.querySelector('[data-ds-copy]');
    if (label) claim(label, 'attrs', 'id', 'hidden');
    if (copyButton) claim(copyButton, 'attrs', 'data-ds-copy');
    const ownCopy = copyButton && (!copyButton.getAttribute('data-ds-copy') || panels.some((panel) => copyButton.getAttribute('data-ds-copy') === `#${panel.code.id}`));
    const head = () => {
    const panel = active();
    if (!panel) return;
    if (label) {
    const word = panel.pre.getAttribute('data-ds-code-label') ?? panel.lang ?? '';
    if (label.textContent !== word) label.textContent = word;
    label.hidden = !word;
    }
    if (ownCopy) copyButton.setAttribute('data-ds-copy', `#${panel.code.id}`);
    if (!panel.pre.hasAttribute('aria-label') && !panel.pre.hasAttribute('aria-labelledby')) {
    const named = tabOf(panel) ?? root.querySelector('.ds-code__title') ?? (label && !label.hidden ? label : null);
    if (named) {
    claim(named, 'attrs', 'id');
    if (!named.id) {
    shared.ids += 1;
    named.id = `ds-code-name-${shared.ids}`;
    }
    panel.pre.setAttribute('aria-labelledby', named.id);
    }
    }
    };
    const expandButton = root.querySelector('[data-ds-code-expand]');
    const expandLabel = expandButton?.querySelector('.ds-code__expand-label');
    if (expandButton) claim(expandButton, 'attrs', 'aria-expanded', 'aria-controls');
    let expanded = false;
    let finishHeight = null;
    const maxLines = () => {
    const value = Number.parseInt(root.getAttribute('data-ds-code-max') ?? '', 10);
    return Number.isInteger(value) && value > 0 ? value : 0;
    };
    const writeState = () => {
    const max = maxLines();
    const folds = max > 0 && (active()?.lines ?? 0) > max;
    if (folds) {
    root.style.setProperty('--ds-code-max', String(max));
    root.setAttribute('data-state', expanded ? 'expanded' : 'collapsed');
    } else {
    root.removeAttribute('data-state');
    root.style.removeProperty('--ds-code-max');
    }
    if (expandButton) {
    expandButton.setAttribute('aria-expanded', String(expanded));
    if (active()) expandButton.setAttribute('aria-controls', active().pre.id || (active().pre.id = `${active().code.id}-block`));
    const words = t(expanded ? 'showLess' : 'showMore');
    if (expandLabel && expandLabel.textContent !== words) expandLabel.textContent = words;
    }
    };
    const glide = (pre, change) => {
    const from = pre.getBoundingClientRect().height;
    finishHeight?.(null, true);
    pre.style.height = `${from}px`;
    change();
    const style = doc.defaultView.getComputedStyle(pre);
    const folded = root.getAttribute('data-state') === 'collapsed';
    const to = folded
    ? maxLines() * Number.parseFloat(style.lineHeight) + Number.parseFloat(style.paddingTop)
    : pre.scrollHeight + Number.parseFloat(style.borderTopWidth) + Number.parseFloat(style.borderBottomWidth);
    void pre.offsetHeight;
    pre.style.height = `${to}px`;
    let timer = 0;
    const done = (event, keep = false) => {
    if (event && (event.target !== pre || event.propertyName !== 'height')) return;
    pre.removeEventListener('transitionend', done);
    globalThis.clearTimeout(timer);
    if (!keep) pre.style.removeProperty('height');
    if (finishHeight === done) finishHeight = null;
    };
    finishHeight = done;
    pre.addEventListener('transitionend', done);
    timer = globalThis.setTimeout(done, motionDuration(pre, folded ? '--ds-dur-base' : '--ds-dur-medium') + motionDuration(pre, '--ds-dur-fast'));
    };
    const setExpanded = (open, animate = true) => {
    const next = Boolean(open);
    if (next === expanded) return;
    const pre = active()?.pre;
    const apply = () => {
    expanded = next;
    writeState();
    };
    if (animate && pre && root.hasAttribute('data-state') && pre.isConnected) glide(pre, apply);
    else apply();
    notify('expand', { expanded });
    };
    const wrapButton = root.querySelector('[data-ds-code-wrap]');
    if (wrapButton) claim(wrapButton, 'attrs', 'aria-pressed', 'aria-label');
    const remember = root.getAttribute('data-ds-code-remember');
    const store = remember ? openStore('code') : null;
    let wrapping = panels.some((panel) => panel.pre.classList.contains('ds-code-block--wrap'));
    if (store) wrapping = store.get(`${remember}.wrap`, wrapping) === true;
    const writeWrap = () => {
    for (const panel of panels) panel.pre.classList.toggle('ds-code-block--wrap', wrapping);
    if (wrapButton) {
    wrapButton.setAttribute('aria-pressed', String(wrapping));
    if (!wrapButton.hasAttribute('aria-label') && !wrapButton.hasAttribute('aria-labelledby') && !wrapButton.textContent.trim()) wrapButton.setAttribute('aria-label', t('wrapLines'));
    }
    };
    const setWrap = (on) => {
    const next = Boolean(on);
    if (next === wrapping) return;
    wrapping = next;
    writeWrap();
    store?.set(`${remember}.wrap`, wrapping);
    notify('wrap', { wrap: wrapping });
    };
    let indicator = null;
    let rover = null;
    const select = (next, focus = false) => {
    if (!Number.isInteger(next) || next < 0 || next >= panels.length) return;
    const tab = tabOf(panels[next]);
    if (tab?.getAttribute('aria-disabled') === 'true') return;
    const changed = next !== index;
    index = next;
    panels.forEach((panel, at) => {
    const shown = at === index;
    const own = tabOf(panel);
    if (own) {
    own.setAttribute('aria-selected', String(shown));
    own.tabIndex = shown ? 0 : -1;
    }
    if (panel.pre.hidden === shown) {
    panel.pre.hidden = !shown;
    if (shown && changed) enter(panel.pre, 'swap');
    }
    });
    head();
    writeState();
    if (focus) tab?.focus();
    if (changed) notify('select', { index, panel: panels[index].pre });
    };
    for (const tab of tabs) claim(tab, 'attrs', 'aria-selected', 'tabindex');
    if (tablist && tabs.length) {
    indicator = makeIndicator(tablist, { active: '[aria-selected="true"]', className: 'ds-tabs__indicator', reveal: true });
    rover = roving(tablist, { onActivate: (tab) => select(panels.findIndex((panel) => tabOf(panel) === tab)) });
    listen(tablist, 'click', (event) => {
    const tab = event.target.closest?.('[role="tab"]');
    if (!tab || !tablist.contains(tab)) return;
    select(panels.findIndex((panel) => tabOf(panel) === tab));
    });
    }
    if (wrapButton) listen(wrapButton, 'click', () => setWrap(!wrapping));
    if (expandButton) listen(expandButton, 'click', () => setExpanded(!expanded));
    panels.forEach(render);
    writeWrap();
    select(index);
    writeState();
    return {
    get() {
    const panel = active();
    if (!panel) return '';
    sync(panel);
    return panel.source;
    },
    set(next) {
    const panel = active();
    if (!panel) return;
    sync(panel);
    const text = String(next ?? '');
    if (text === panel.source) return;
    panel.source = text;
    render(panel);
    writeState();
    notify('change', { code: text, index });
    },
    on(type, fn) {
    if (!listeners.has(type)) listeners.set(type, new Set());
    listeners.get(type).add(fn);
    return () => listeners.get(type)?.delete(fn);
    },
    select(next) { select(next); },
    expand(open = true) { setExpanded(open); },
    wrap(on = true) { setWrap(on); },
    refresh() {
    panels.forEach(sync);
    panels.forEach(render);
    head();
    writeState();
    },
    update() { this.refresh(); },
    destroy() {
    finishHeight?.(null);
    indicator?.destroy?.();
    rover?.destroy?.();
    cleanups.forEach((fn) => fn());
    for (const [el, kind, name] of claims) disown(el, kind, name);
    listeners.clear();
    },
    };
    }
    define('code', {
    attribute: 'data-ds-code',
    attributes: ['data-ds-code-max', 'data-ds-code-start', 'data-ds-code-mark'],
    init(el, ctx) {
    return controller(el, {}, ctx.signal);
    },
    });
    function create(el, options = {}) {
    return controller(el, options, null);
    }

    return Object.freeze({
      parseLines: parseLines,
      linesHtml: linesHtml,
      create: create
    });
  });

  add('lightbox', ['MAX_ZOOM', 'nextZoom', 'clampPan', 'create', 'open', 'close', 'show'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const enter = DS.motion.enter, motionDuration = DS.motion.motionDuration;
    const chevronLeft = DS.icons.chevronLeft, chevronRight = DS.icons.chevronRight, closeIcon = DS.icons.close, alertCircle = DS.icons.alertCircle, minimize = DS.icons.minimize, plus = DS.icons.plus, iconElement = DS.icons.iconElement;
    const openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close, internals = DS.overlay.internals;
    const announce = DS.a11y.announce, roving = DS.a11y.roving;
    const { overlays } = internals;
    const MAX_ZOOM = 4;
    const ITEMS = '.ds-lightbox__item, [data-ds-lightbox-item]';
    function nextZoom(zoom, direction) {
    if (direction === 0) return 1;
    const next = direction > 0 ? zoom * 2 : zoom / 2;
    return Math.min(MAX_ZOOM, Math.max(1, Math.round(next * 100) / 100));
    }
    function clampPan(x, y, size, frame, zoom) {
    const reach = (length, room) => Math.max(0, (length * zoom - room) / 2);
    const rx = reach(size.width, frame.width);
    const ry = reach(size.height, frame.height);
    const clamp = (value, limit) => (limit === 0 ? 0 : Math.round(Math.min(limit, Math.max(-limit, value))));
    return { x: clamp(x, rx), y: clamp(y, ry) };
    }
    function imagesOf(gallery) {
    return [...gallery.querySelectorAll(ITEMS)].map((item) => {
    const thumb = item.querySelector('img');
    return {
    src: item.getAttribute('data-src') ?? item.getAttribute('href') ?? thumb?.getAttribute('src') ?? '',
    alt: thumb?.getAttribute('alt') || item.getAttribute('aria-label') || '',
    caption: item.getAttribute('data-caption') ?? '',
    thumb: thumb?.getAttribute('src') ?? '',
    item,
    };
    });
    }
    function buildViewer(home, label) {
    const doc = home.ownerDocument;
    const make = (tag, className) => {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    return node;
    };
    const button = (className, name, glyph) => {
    const node = make('button', `ds-btn ds-btn--ghost ds-btn--icon${className ? ` ${className}` : ''}`);
    node.type = 'button';
    node.setAttribute('aria-label', name);
    node.append(iconElement(glyph, 20, undefined, doc));
    return node;
    };
    const native = typeof make('dialog').showModal === 'function';
    const dialog = make(native ? 'dialog' : 'div', 'ds-dialog ds-lightbox');
    const box = make('div', 'ds-dialog__box ds-lightbox__box');
    if (native) dialog.setAttribute('aria-label', label);
    else {
    dialog.hidden = true;
    box.setAttribute('role', 'dialog');
    box.setAttribute('aria-modal', 'true');
    box.setAttribute('aria-label', label);
    }
    const backdrop = make('div', 'ds-dialog__backdrop');
    backdrop.setAttribute('data-ds-close', '');
    const bar = make('div', 'ds-lightbox__bar');
    const count = make('p', 'ds-lightbox__count');
    const zoomOut = button('', t('zoomOut'), minimize);
    const zoomIn = button('', t('zoomIn'), plus);
    const closer = button('', t('close'), closeIcon);
    closer.setAttribute('data-ds-close', '');
    bar.append(count, zoomOut, zoomIn, closer);
    const stage = make('div', 'ds-lightbox__stage');
    const prev = button('ds-lightbox__nav ds-lightbox__nav--prev', t('previous'), chevronLeft);
    const next = button('ds-lightbox__nav ds-lightbox__nav--next', t('next'), chevronRight);
    const figure = make('figure', 'ds-lightbox__figure');
    const frame = make('div', 'ds-lightbox__frame');
    const image = (make('img', 'ds-lightbox__image'));
    image.draggable = false;
    image.decoding = 'async';
    const spinner = make('span', 'ds-spinner');
    spinner.setAttribute('aria-hidden', 'true');
    const error = make('p', 'ds-lightbox__error');
    error.append(iconElement(alertCircle, 20, undefined, doc), doc.createTextNode(t('imageFailed')));
    frame.append(image, spinner, error);
    const caption = make('figcaption', 'ds-lightbox__caption');
    figure.append(frame, caption);
    stage.append(prev, figure, next);
    const thumbs = make('div', 'ds-lightbox__thumbs');
    thumbs.setAttribute('role', 'group');
    thumbs.setAttribute('aria-label', t('images'));
    box.append(bar, stage, thumbs);
    dialog.append(backdrop, box);
    const theme = home.closest('.ds-theme');
    const parent = !theme || theme === doc.documentElement ? doc.body : theme;
    parent.append(dialog);
    return { dialog, box, count, zoomIn, zoomOut, closer, prev, next, figure, frame, image, caption, thumbs };
    }
    const OPTIONS = ['--ds-size-step', '--ds-size-font', '--ds-density', '--ds-shape', '--ds-target-min'];
    function carryOptions(home, dialog) {
    const view = dialog.ownerDocument.defaultView;
    if (!view?.getComputedStyle || !home?.isConnected || !dialog.style) return;
    for (const name of OPTIONS) dialog.style.removeProperty(name);
    const there = view.getComputedStyle(dialog);
    const inherited = OPTIONS.map((name) => there.getPropertyValue(name).trim());
    const here = view.getComputedStyle(home);
    OPTIONS.forEach((name, index) => {
    const value = here.getPropertyValue(name).trim();
    if (value && value !== inherited[index]) dialog.style.setProperty(name, value);
    });
    }
    const controllers = new WeakMap();
    function create(gallery, options = {}) {
    const known = controllers.get(gallery);
    if (known) return known;
    const abort = new AbortController();
    const signal = abort.signal;
    const listeners = [];
    const list = () => (typeof options.images === 'function' ? options.images() : imagesOf(gallery));
    const state = { index: 0, zoom: 1, x: 0, y: 0, images: ([]), viewer: (null), rover: (null), pointers: new Map(), gesture: (null), lastTap: 0 };
    const homeOf = () => options.home ?? (gallery.isConnected ? gallery : gallery.ownerDocument?.body ?? globalThis.document.body);
    const viewer = () => {
    if (state.viewer?.dialog.isConnected) return state.viewer;
    const label = options.label ?? gallery.getAttribute?.('aria-label') ?? t('images');
    state.viewer = buildViewer(homeOf(), label);
    wireViewer(state.viewer);
    return state.viewer;
    };
    const isOpen = () => Boolean(state.viewer) && overlays.get(state.viewer.dialog)?.phase === 'open';
    const measure = () => {
    const { frame, image } = state.viewer;
    const box = frame.getBoundingClientRect();
    return { frame: { width: box.width, height: box.height }, size: { width: image.offsetWidth, height: image.offsetHeight } };
    };
    const paintTransform = () => {
    const { frame } = state.viewer;
    frame.style.setProperty('--ds-lightbox-zoom', String(state.zoom));
    frame.style.setProperty('--ds-lightbox-x', `${state.x}px`);
    frame.style.setProperty('--ds-lightbox-y', `${state.y}px`);
    if (state.zoom > 1) frame.setAttribute('data-zoomed', '');
    else frame.removeAttribute('data-zoomed');
    state.viewer.zoomOut.setAttribute('aria-disabled', String(state.zoom <= 1));
    state.viewer.zoomIn.setAttribute('aria-disabled', String(state.zoom >= MAX_ZOOM));
    };
    const zoomTo = (zoom, point = { x: 0, y: 0 }) => {
    if (!state.viewer) return;
    const next = Math.min(MAX_ZOOM, Math.max(1, zoom));
    const ratio = next / state.zoom;
    const x = point.x - (point.x - state.x) * ratio;
    const y = point.y - (point.y - state.y) * ratio;
    const before = state.zoom;
    state.zoom = next;
    const { frame, size } = measure();
    const pan = clampPan(x, y, size, frame, next);
    state.x = pan.x;
    state.y = pan.y;
    paintTransform();
    if (before !== next) emit(gallery, 'zoom', { zoom: next });
    };
    const panBy = (dx, dy) => {
    const { frame, size } = measure();
    const pan = clampPan(state.x + dx, state.y + dy, size, frame, state.zoom);
    state.x = pan.x;
    state.y = pan.y;
    paintTransform();
    };
    const paintThumbs = () => {
    const { thumbs } = state.viewer;
    const doc = thumbs.ownerDocument;
    const total = state.images.length;
    if (total < 2) {
    thumbs.replaceChildren();
    return;
    }
    if (thumbs.children.length !== total) {
    thumbs.replaceChildren(...state.images.map((image, index) => {
    const node = doc.createElement('button');
    node.type = 'button';
    node.className = 'ds-lightbox__thumb';
    node.setAttribute('aria-label', image.alt ? `${t('imageOf', { index: index + 1, total })}: ${image.alt}` : t('imageOf', { index: index + 1, total }));
    const img = doc.createElement('img');
    img.alt = '';
    img.loading = 'lazy';
    img.src = image.thumb || image.src;
    node.append(img);
    return node;
    }));
    }
    [...thumbs.children].forEach((node, index) => {
    if (index === state.index) node.setAttribute('aria-current', 'true');
    else node.removeAttribute('aria-current');
    });
    const current = (thumbs.children[state.index]);
    current?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
    };
    const show = (index, say = true) => {
    const v = viewer();
    const total = state.images.length;
    if (!total) return;
    state.index = ((index % total) + total) % total;
    const image = state.images[state.index];
    state.zoom = 1;
    state.x = 0;
    state.y = 0;
    paintTransform();
    v.frame.removeAttribute('data-failed');
    v.frame.setAttribute('data-loading', '');
    v.image.alt = image.alt ?? '';
    const done = () => v.frame.removeAttribute('data-loading');
    v.image.onload = done;
    v.image.onerror = () => {
    done();
    v.frame.setAttribute('data-failed', '');
    };
    if (v.image.getAttribute('src') !== image.src) v.image.src = image.src;
    else if (v.image.complete) done();
    v.caption.textContent = image.caption ?? '';
    v.count.textContent = t('imageOf', { index: state.index + 1, total });
    v.prev.hidden = total < 2;
    v.next.hidden = total < 2;
    v.next.toggleAttribute('autofocus', total >= 2);
    v.closer.toggleAttribute('autofocus', total < 2);
    paintThumbs();
    if (say) {
    enter(v.figure, 'swap');
    announce(`${t('imageOf', { index: state.index + 1, total })}. ${image.caption || image.alt || ''}`.trim());
    emit(gallery, 'change', { index: state.index, total });
    }
    };
    function wireViewer(v) {
    v.zoomIn.addEventListener('click', () => {
    if (v.zoomIn.getAttribute('aria-disabled') !== 'true') zoomTo(nextZoom(state.zoom, 1));
    }, { signal });
    v.zoomOut.addEventListener('click', () => {
    if (v.zoomOut.getAttribute('aria-disabled') !== 'true') zoomTo(nextZoom(state.zoom, -1));
    }, { signal });
    v.prev.addEventListener('click', () => show(state.index - 1), { signal });
    v.next.addEventListener('click', () => show(state.index + 1), { signal });
    v.thumbs.addEventListener('click', (event) => {
    const node = (event.target)?.closest?.('.ds-lightbox__thumb');
    if (node) show([...v.thumbs.children].indexOf(node));
    }, { signal });
    state.rover = roving(v.thumbs, { items: '.ds-lightbox__thumb', orientation: 'horizontal', wrap: true, onActivate: (node) => {
    const index = [...v.thumbs.children].indexOf(node);
    if (index >= 0 && index !== state.index) show(index);
    } });
    v.dialog.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    if ( (event.target)?.closest?.('.ds-lightbox__thumbs')) return;
    const step = Math.round(v.frame.getBoundingClientRect().width / 10);
    const zoomed = state.zoom > 1;
    const keys = {
    ArrowLeft: () => (zoomed ? panBy(step, 0) : show(state.index - 1)),
    ArrowRight: () => (zoomed ? panBy(-step, 0) : show(state.index + 1)),
    ArrowUp: () => zoomed && panBy(0, step),
    ArrowDown: () => zoomed && panBy(0, -step),
    Home: () => show(0),
    End: () => show(state.images.length - 1),
    '+': () => zoomTo(nextZoom(state.zoom, 1)),
    '=': () => zoomTo(nextZoom(state.zoom, 1)),
    '-': () => zoomTo(nextZoom(state.zoom, -1)),
    0: () => zoomTo(1),
    Escape: () => zoomTo(1),
    };
    const run = keys[event.key];
    if (!run) return;
    if (event.key === 'Escape' && !zoomed) return;
    if ((event.key === 'ArrowUp' || event.key === 'ArrowDown') && !zoomed) return;
    event.preventDefault();
    run();
    }, { signal });
    const frame = v.frame;
    const centre = (event) => {
    const box = frame.getBoundingClientRect();
    return { x: event.clientX - box.left - box.width / 2, y: event.clientY - box.top - box.height / 2 };
    };
    frame.addEventListener('dblclick', (event) => zoomTo(state.zoom > 1 ? 1 : 2, centre(event)), { signal });
    frame.addEventListener('wheel', ( event) => {
    if (!event.ctrlKey) return;
    event.preventDefault();
    zoomTo(state.zoom * Math.exp(-event.deltaY / 200), centre(event));
    }, { signal, passive: false });
    frame.addEventListener('pointerdown', ( event) => {
    if (event.button !== 0 || state.images.length === 0) return;
    try {
    frame.setPointerCapture?.(event.pointerId);
    } catch {
    }
    state.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (state.pointers.size > 2) return;
    if (state.pointers.size === 2) {
    const [a, b] = [...state.pointers.values()];
    state.gesture = { kind: 'pinch', distance: Math.hypot(a.x - b.x, a.y - b.y) || 1, zoom: state.zoom };
    return;
    }
    state.gesture = { kind: state.zoom > 1 ? 'pan' : 'swipe', start: { x: event.clientX, y: event.clientY }, from: { x: state.x, y: state.y }, touch: event.pointerType !== 'mouse', moved: false };
    }, { signal });
    frame.addEventListener('pointermove', ( event) => {
    if (!state.pointers.has(event.pointerId) || !state.gesture) return;
    state.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    const gesture = state.gesture;
    if (gesture.kind === 'pinch') {
    if (state.pointers.size !== 2) return;
    const [a, b] = [...state.pointers.values()];
    frame.setAttribute('data-dragging', '');
    zoomTo(gesture.zoom * (Math.hypot(a.x - b.x, a.y - b.y) / gesture.distance));
    return;
    }
    const dx = event.clientX - gesture.start.x;
    const dy = event.clientY - gesture.start.y;
    if (!gesture.moved && Math.hypot(dx, dy) < 4) return;
    gesture.moved = true;
    frame.setAttribute('data-dragging', '');
    if (gesture.kind === 'pan') {
    const { frame: room, size } = measure();
    const pan = clampPan(gesture.from.x + dx, gesture.from.y + dy, size, room, state.zoom);
    state.x = pan.x;
    state.y = pan.y;
    paintTransform();
    } else if (gesture.touch && state.images.length > 1) {
    state.x = Math.round(dx);
    paintTransform();
    }
    }, { signal });
    const end = ( event) => {
    if (!state.pointers.delete(event.pointerId)) return;
    const gesture = state.gesture;
    if (state.pointers.size > 0) {
    if (gesture?.kind === 'pinch' && state.pointers.size === 1) {
    const [rest] = [...state.pointers.values()];
    state.gesture = { kind: state.zoom > 1 ? 'pan' : 'swipe', start: { x: rest.x, y: rest.y }, from: { x: state.x, y: state.y }, touch: true, moved: true };
    } else if (gesture?.kind === 'pinch' && state.pointers.size === 2) {
    const [a, b] = [...state.pointers.values()];
    state.gesture = { kind: 'pinch', distance: Math.hypot(a.x - b.x, a.y - b.y) || 1, zoom: state.zoom };
    }
    return;
    }
    state.gesture = null;
    frame.removeAttribute('data-dragging');
    if (!gesture) return;
    if (gesture.kind === 'swipe' && gesture.touch) {
    const dx = event.clientX - gesture.start.x;
    const width = frame.getBoundingClientRect().width;
    if (gesture.moved && Math.abs(dx) > width / 5 && state.images.length > 1) show(state.index + (dx < 0 ? 1 : -1));
    else if (gesture.moved) {
    state.x = 0;
    paintTransform();
    } else {
    const now = event.timeStamp;
    if (now - state.lastTap < motionDuration(frame, '--ds-delay-dismiss')) zoomTo(state.zoom > 1 ? 1 : 2, centre(event));
    state.lastTap = now;
    }
    }
    };
    frame.addEventListener('pointerup', end, { signal });
    frame.addEventListener('pointercancel', end, { signal });
    v.dialog.addEventListener('ds:close', (event) => {
    if (event.target !== v.dialog) return;
    emit(gallery, 'close', { index: state.index });
    }, { signal });
    }
    if (typeof gallery.addEventListener === 'function') {
    gallery.addEventListener('click', ( event) => {
    if (event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const item = (event.target)?.closest?.(ITEMS);
    if (!item || !gallery.contains(item)) return;
    event.preventDefault();
    const index = [...gallery.querySelectorAll(ITEMS)].indexOf(item);
    controller.open(index, item);
    }, { signal });
    }
    const controller = {
    element: gallery,
    get: () => ({ open: isOpen(), index: state.index, total: state.images.length, zoom: state.zoom }),
    set( value) {
    const next = typeof value === 'number' ? { index: value } : value ?? {};
    if (typeof next.index === 'number') {
    if (!state.images.length) state.images = list();
    show(next.index, isOpen());
    }
    if (typeof next.zoom === 'number' && state.viewer) zoomTo(next.zoom);
    },
    open( index = 0, opener = null) {
    state.images = list();
    if (!state.images.length) return Promise.resolve();
    const v = viewer();
    carryOptions(homeOf(), v.dialog);
    show(index, false);
    const opened = openOverlay(v.dialog, { opener });
    announce(`${t('imageOf', { index: state.index + 1, total: state.images.length })}. ${state.images[state.index].caption || state.images[state.index].alt || ''}`.trim());
    emit(gallery, 'open', { index: state.index });
    return opened;
    },
    close: () => (state.viewer ? closeOverlay(state.viewer.dialog) : Promise.resolve()),
    next: () => show(state.index + 1),
    prev: () => show(state.index - 1),
    on( type, fn) {
    if (typeof gallery.addEventListener !== 'function') return () => {};
    const name = `ds:${type}`;
    gallery.addEventListener(name, fn);
    const off = () => gallery.removeEventListener(name, fn);
    listeners.push(off);
    return off;
    },
    destroy() {
    if (controllers.get(gallery) !== controller) return;
    controllers.delete(gallery);
    abort.abort();
    state.rover?.destroy?.();
    for (const off of listeners.splice(0)) off();
    const dialog = state.viewer?.dialog;
    state.viewer = null;
    if (!dialog) return;
    const phase = overlays.get(dialog);
    if (phase?.phase === 'open') closeOverlay(dialog).then(() => dialog.remove());
    else if (phase?.phase === 'closing') Promise.resolve(phase.closing).then(() => dialog.remove());
    else dialog.remove();
    },
    };
    controllers.set(gallery, controller);
    return controller;
    }
    function open(gallery, options = {}) {
    return create(gallery).open(options.index ?? 0, options.opener ?? null);
    }
    function close(gallery) {
    return controllers.get(gallery)?.close() ?? Promise.resolve();
    }
    function show(images, options = {}) {
    const doc = options.opener?.ownerDocument ?? globalThis.document;
    const home = options.opener ?? doc.querySelector('.ds-theme') ?? doc.body;
    const key = doc.createElement('div');
    if (options.label) key.setAttribute('aria-label', options.label);
    const controller = create(key, { images: () => images.slice(), label: options.label, home });
    key.addEventListener('ds:close', () => controller.destroy(), { once: true });
    controller.open(options.index ?? 0, options.opener ?? null);
    return controller;
    }
    define('lightbox', {
    attribute: 'data-ds-lightbox',
    init: (element) => create(element),
    });

    return Object.freeze({
      MAX_ZOOM: MAX_ZOOM,
      nextZoom: nextZoom,
      clampPan: clampPan,
      create: create,
      open: open,
      close: close,
      show: show
    });
  });

  add('list', ['rangeKeys', 'allState', 'create', 'facts'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const motionDuration = DS.motion.motionDuration;
    const announce = DS.a11y.announce;
    const own = DS.dom.own, disown = DS.dom.disown;
    const virtualOf = DS.virtual.create;
    const draggable = DS.dnd.create;
    const copyGlyph = DS.icons.copy, checkGlyph = DS.icons.check, gripGlyph = DS.icons.grip, iconElement = DS.icons.iconElement;
    const ITEM = '.ds-list__item';
    const CHECK = '.ds-list__check';
    const CONTROLS = 'a[href], button, input, select, textarea, label, summary, [tabindex]';
    function rangeKeys(keys, anchor, target) {
    const to = keys.indexOf(target);
    if (to < 0) return [];
    const from = anchor === null ? -1 : keys.indexOf(anchor);
    if (from < 0) return [target];
    return keys.slice(Math.min(from, to), Math.max(from, to) + 1);
    }
    function allState(checked, total) {
    if (!total || !checked) return 'false';
    return checked >= total ? 'true' : 'mixed';
    }
    async function copyText(doc, text) {
    const view = doc.defaultView;
    try {
    if (view?.navigator?.clipboard?.writeText && view.isSecureContext !== false) {
    await view.navigator.clipboard.writeText(text);
    return true;
    }
    } catch {
    }
    const area = doc.createElement('textarea');
    area.value = text;
    area.readOnly = true;
    area.className = 'ds-sr';
    const back = doc.activeElement;
    (doc.querySelector('.ds-theme') ?? doc.body).append(area);
    area.select();
    let ok = false;
    try {
    ok = doc.execCommand('copy');
    } catch {
    ok = false;
    }
    area.remove();
    if (back && typeof back.focus === 'function') back.focus({ preventScroll: true });
    return ok;
    }
    const lists = new WeakMap();
    const factsInstances = new WeakMap();
    const modeOf = (value) => (value === 'multiple' || value === 'single' ? value : null);
    const flag = (value) => value !== null && value !== undefined && value !== 'false';
    function controller(list, initial) {
    const doc = list.ownerDocument;
    const abort = new AbortController();
    const signal = abort.signal;
    let options = { ...initial, select: modeOf(initial.select) };
    const selected = new Set();
    let anchor = null;
    let dnd = null;
    let windowed = null;
    const wired = new WeakSet();
    const saved = new WeakMap();
    const put = (element, name, value) => {
    let names = saved.get(element);
    if (value === null) {
    if (!names?.has(name)) return;
    const before = names.get(name);
    names.delete(name);
    disown(element, 'attrs', name);
    if (before === null) element.removeAttribute(name);
    else element.setAttribute(name, before);
    return;
    }
    if (!names) saved.set(element, (names = new Map()));
    if (!names.has(name)) {
    names.set(name, element.getAttribute(name));
    own(element, 'attrs', name);
    }
    if (element.getAttribute(name) !== value) element.setAttribute(name, value);
    };
    const asListbox = () => Boolean(windowed) && Boolean(options.select);
    const rows = () => Array.from(list.querySelectorAll(`:scope > ${ITEM}`));
    const boxOf = (row) => row.querySelector(`:scope > ${CHECK}, :scope ${CHECK}`);
    const keyOf = (row) => row?.getAttribute('data-ds-key') ?? boxOf(row)?.value ?? null;
    const labelOf = (row) => (row.querySelector('.ds-list__title')?.textContent ?? row.textContent ?? '').replace(/\s+/g, ' ').trim();
    const allBoxes = () => {
    if (!list.id) return [];
    return Array.from(doc.querySelectorAll('input[data-ds-list-all]')).filter((box) => (box.getAttribute('aria-controls') ?? '').split(/\s+/).includes(list.id));
    };
    const keys = () => {
    if (windowed && typeof options.virtual?.key === 'function') {
    const count = windowed.get().count;
    return Array.from({ length: count }, (_, index) => String(options.virtual.key(index)));
    }
    return rows().filter((row) => boxOf(row) && !boxOf(row).disabled).map(keyOf).filter((key) => key !== null);
    };
    const sync = () => {
    const listbox = asListbox();
    put(list, 'role', listbox ? 'listbox' : null);
    put(list, 'aria-multiselectable', listbox && options.select === 'multiple' ? 'true' : null);
    for (const row of rows()) {
    const box = boxOf(row);
    const key = keyOf(row);
    if (box && key !== null) box.checked = selected.has(key);
    put(row, 'role', listbox ? 'option' : null);
    put(row, 'aria-selected', listbox ? String(key !== null && selected.has(key)) : null);
    if (box) put(box, 'inert', listbox ? '' : null);
    }
    if (options.select === 'multiple') {
    const all = keys();
    const state = allState(all.filter((key) => selected.has(key)).length, all.length);
    for (const box of allBoxes()) {
    if (!wired.has(box)) {
    wired.add(box);
    box.addEventListener('change', onAll, { signal });
    }
    box.checked = state === 'true';
    box.indeterminate = state === 'mixed';
    }
    }
    if (options.reorder) addGrips();
    };
    const change = (detail, many = 0) => {
    sync();
    if (many > 1) announce(t('listSelected', { count: selected.size }));
    emit(list, 'change', { keys: handle.get(), ...detail });
    };
    const read = () => {
    for (const row of rows()) {
    const box = boxOf(row);
    const key = keyOf(row);
    if (!box || key === null) continue;
    if (box.checked) {
    if (options.select === 'single') selected.clear();
    selected.add(key);
    }
    }
    };
    const toggle = (key, checked, shift) => {
    if (options.select === 'single') {
    selected.clear();
    if (checked) selected.add(key);
    anchor = key;
    change({ key, checked });
    return;
    }
    const range = shift ? rangeKeys(keys(), anchor, key) : [key];
    for (const each of range) {
    if (checked) selected.add(each);
    else selected.delete(each);
    }
    anchor = key;
    change({ key, checked }, range.length);
    };
    const onClick = ( event) => {
    if (!options.select) return;
    const target = event.target;
    const box = target.closest?.(CHECK);
    const row = target.closest?.(ITEM);
    if (!row || row.parentElement !== list) return;
    if (box) {
    const key = keyOf(row);
    if (key !== null) toggle(key, box.checked, event.shiftKey);
    return;
    }
    const control = target.closest(CONTROLS);
    if (row.querySelector('.ds-list__action') || (control && control !== row)) return;
    flip(row, event.shiftKey);
    };
    const flip = (row, shift) => {
    const box = boxOf(row);
    if (!box || box.disabled) return;
    if (options.select === 'single' && box.checked) return;
    box.checked = !box.checked;
    const key = keyOf(row);
    if (key !== null) toggle(key, box.checked, shift);
    };
    const onKey = ( event) => {
    if (!options.select || event.key !== ' ' || event.defaultPrevented) return;
    const row = event.target;
    if (!row?.matches?.(ITEM) || row.parentElement !== list) return;
    event.preventDefault();
    flip(row, event.shiftKey);
    };
    const onAll = ( event) => {
    const box = event.target;
    if (options.select !== 'multiple' || !allBoxes().includes(box)) return;
    const all = keys();
    if (box.checked) for (const key of all) selected.add(key);
    else selected.clear();
    change({ key: null, checked: box.checked }, all.length);
    };
    const addGrips = () => {
    for (const row of rows()) {
    if (row.querySelector(':scope > .ds-list__grip')) continue;
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-sm ds-list__grip';
    button.setAttribute('data-ds-list-own', '');
    button.setAttribute('aria-label', t('listMove', { label: labelOf(row) }));
    button.append(iconElement(gripGlyph, 18, undefined, doc));
    row.prepend(button);
    }
    };
    const startDrag = () => {
    if (dnd || !options.reorder || windowed) return;
    addGrips();
    dnd = draggable(list, { items: ITEM, handle: '.ds-list__grip', label: labelOf });
    };
    const stopDrag = () => {
    dnd?.destroy();
    dnd = null;
    for (const grip of list.querySelectorAll('.ds-list__grip[data-ds-list-own]')) grip.remove();
    };
    const startWindow = () => {
    if (windowed || !options.virtual) return;
    const { scroller, ...rest } = options.virtual;
    const host = scroller ?? list;
    windowed = virtualOf(host, { content: list, ...rest });
    host.addEventListener('ds:range', sync, { signal });
    };
    list.addEventListener('click', onClick, { signal });
    list.addEventListener('keydown', onKey, { signal });
    const watcher = typeof doc.defaultView?.MutationObserver === 'function' ? new doc.defaultView.MutationObserver(() => sync()) : null;
    watcher?.observe(list, { childList: true });
    read();
    startWindow();
    startDrag();
    sync();
    const handle = {
    get() {
    if (options.select === 'multiple') {
    const all = keys();
    const known = new Set(all);
    return [...all.filter((key) => selected.has(key)), ...[...selected].filter((key) => !known.has(key))];
    }
    if (options.select === 'single') return selected.values().next().value ?? null;
    return null;
    },
    set(value) {
    selected.clear();
    const list = Array.isArray(value) ? value : value === null || value === undefined ? [] : [value];
    for (const key of options.select === 'single' ? list.slice(0, 1) : list) selected.add(String(key));
    sync();
    },
    on(type, fn) {
    const listener = ( event) => {
    if (event.target === list || event.target === (options.virtual?.scroller ?? list)) fn(event.detail);
    };
    list.addEventListener(`ds:${type}`, listener, { signal });
    return () => list.removeEventListener(`ds:${type}`, listener);
    },
    window() {
    return windowed;
    },
    configure(next = {}) {
    options = { ...options, ...next, select: modeOf(next.select === undefined ? options.select : next.select) };
    if (next.virtual && !windowed) startWindow();
    else if (next.virtual && windowed) windowed.set({ count: next.virtual.count ?? windowed.get().count });
    if (options.reorder) startDrag();
    else stopDrag();
    sync();
    },
    update() {
    handle.configure(attributeOptions(list));
    },
    destroy() {
    if (signal.aborted) return;
    abort.abort();
    watcher?.disconnect();
    stopDrag();
    windowed?.destroy();
    windowed = null;
    put(list, 'role', null);
    put(list, 'aria-multiselectable', null);
    for (const row of rows()) {
    put(row, 'role', null);
    put(row, 'aria-selected', null);
    const box = boxOf(row);
    if (box) put(box, 'inert', null);
    }
    if (lists.get(list) === handle) lists.delete(list);
    },
    };
    lists.set(list, handle);
    return handle;
    }
    const attributeOptions = (el) => ({ select: el.getAttribute('data-ds-list-select'), reorder: flag(el.getAttribute('data-ds-list-reorder')) });
    define('list', {
    attribute: 'data-ds-list',
    attributes: ['data-ds-list-select', 'data-ds-list-reorder'],
    init(el) {
    return lists.get(el) ?? controller(el, attributeOptions(el));
    },
    });
    function create(el, options = {}) {
    if (!el?.ownerDocument) return { get: () => null, set() {}, on: () => () => {}, window: () => null, configure() {}, update() {}, destroy() {} };
    const existing = lists.get(el);
    if (existing) {
    existing.configure(options);
    return existing;
    }
    return controller(el, { ...attributeOptions(el), ...options });
    }
    function factsController(dl) {
    const doc = dl.ownerDocument;
    const abort = new AbortController();
    const signal = abort.signal;
    const timers = new Map();
    const labelOf = (dd) => {
    let dt = dd.previousElementSibling;
    while (dt && dt.tagName !== 'DT') dt = dt.previousElementSibling;
    return (dt?.textContent ?? '').replace(/\s+/g, ' ').trim();
    };
    const textOf = (dd) => {
    const given = dd.getAttribute('data-ds-facts-value');
    if (given !== null) return given;
    const copy = dd.cloneNode(true);
    for (const extra of copy.querySelectorAll('.ds-facts__copy, .ds-lock')) extra.remove();
    return (copy.textContent ?? '').replace(/\s+/g, ' ').trim();
    };
    const sync = () => {
    for (const dd of dl.querySelectorAll('dd')) {
    if (dd.closest('dl') !== dl) continue;
    const mine = dd.querySelector(':scope > .ds-facts__copy');
    if (dd.hasAttribute('data-ds-facts-nocopy')) {
    mine?.remove();
    continue;
    }
    if (mine) continue;
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-xs ds-facts__copy';
    const label = t('factsCopy', { label: labelOf(dd) });
    button.setAttribute('aria-label', label);
    button.setAttribute('data-tip', label);
    button.append(iconElement(copyGlyph, 16, undefined, doc), iconElement(checkGlyph, 16, undefined, doc));
    dd.append(button);
    }
    };
    const onClick = async ( event) => {
    const button = event.target.closest?.('.ds-facts__copy');
    if (!button || !dl.contains(button)) return;
    const dd = button.closest('dd');
    const text = textOf(dd);
    const ok = await copyText(doc, text);
    if (!ok || signal.aborted) return;
    button.setAttribute('data-state', 'copied');
    doc.defaultView.clearTimeout(timers.get(button));
    timers.set(button, doc.defaultView.setTimeout(() => button.removeAttribute('data-state'), motionDuration(button, '--ds-hold-status')));
    announce(t('copied'));
    emit(dl, 'copy', { text, label: labelOf(dd) });
    };
    dl.addEventListener('click', onClick, { signal });
    const watcher = typeof doc.defaultView?.MutationObserver === 'function' ? new doc.defaultView.MutationObserver(() => sync()) : null;
    watcher?.observe(dl, { childList: true, subtree: true });
    sync();
    const handle = {
    get() {
    return Array.from(dl.querySelectorAll('dd')).filter((dd) => dd.closest('dl') === dl).map((dd) => [labelOf(dd), textOf(dd)]);
    },
    set() {
    sync();
    },
    on(type, fn) {
    const listener = ( event) => fn(event.detail);
    dl.addEventListener(`ds:${type}`, listener, { signal });
    return () => dl.removeEventListener(`ds:${type}`, listener);
    },
    update() {
    sync();
    },
    destroy() {
    if (signal.aborted) return;
    abort.abort();
    watcher?.disconnect();
    for (const timer of timers.values()) doc.defaultView?.clearTimeout(timer);
    for (const button of dl.querySelectorAll('.ds-facts__copy')) button.remove();
    if (factsInstances.get(dl) === handle) factsInstances.delete(dl);
    },
    };
    factsInstances.set(dl, handle);
    return handle;
    }
    define('facts', {
    attribute: 'data-ds-facts-copy',
    init(el) {
    return factsInstances.get(el) ?? factsController(el);
    },
    });
    function facts(el) {
    if (!el?.ownerDocument) return { get: () => [], set() {}, on: () => () => {}, update() {}, destroy() {} };
    return factsInstances.get(el) ?? factsController(el);
    }

    return Object.freeze({
      rangeKeys: rangeKeys,
      allState: allState,
      create: create,
      facts: facts
    });
  });

  add('listbox', ['VIRTUAL_FROM', 'fuzzy', 'filter', 'highlight', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit;
    const motionDuration = DS.motion.motionDuration;
    const morphHtml = DS.dom.morphHtml, own = DS.dom.own, disown = DS.dom.disown, scrollEdges = DS.dom.scrollEdges;
    const typeaheadMatch = DS.a11y.typeaheadMatch;
    const createWindow = DS.virtual.create;
    const icon = DS.icons.icon, check = DS.icons.check;
    const VIRTUAL_FROM = 100;
    const PAGE = 10;
    const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
    const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ESCAPES[char]);
    const controllers = new WeakMap();
    let sequence = 0;
    function fold(text) {
    const chars = [];
    const at = [];
    const source = String(text ?? '');
    let index = 0;
    for (const char of source) {
    const folded = char.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
    for (const part of folded) {
    chars.push(part);
    at.push(index);
    }
    index += char.length;
    }
    return { text: chars.join(''), at, length: source.length };
    }
    const isWordStart = (text, index) => index === 0 || /[\s\-_/.,:;()[\]]/.test(text[index - 1]) || (/[a-z]/.test(text[index - 1]) && /[0-9]/.test(text[index]));
    function canFinish(token, k, target, pos) {
    let at = pos;
    for (let i = k; i < token.length; i += 1) {
    at = target.indexOf(token[i], at + 1);
    if (at < 0) return false;
    }
    return true;
    }
    function matchToken(token, target) {
    const found = target.indexOf(token);
    if (found >= 0) {
    let score = 100 - Math.min(found, 40);
    if (found === 0) score += 60;
    else if (isWordStart(target, found)) score += 30;
    return { score: score + token.length * 4, ranges: [[found, found + token.length]] };
    }
    const ranges = [];
    let score = 0;
    let last = -1;
    for (let k = 0; k < token.length; k += 1) {
    const char = token[k];
    let index = target.indexOf(char, last + 1);
    if (index < 0) return null;
    if (index !== last + 1 && !isWordStart(target, index)) {
    for (let i = index + 1; i < target.length; i += 1) {
    if (target[i] === char && isWordStart(target, i) && canFinish(token, k + 1, target, i)) {
    index = i;
    break;
    }
    }
    }
    if (ranges.length && index === last + 1) {
    ranges[ranges.length - 1][1] = index + 1;
    score += 6;
    } else {
    ranges.push([index, index + 1]);
    score += isWordStart(target, index) ? 8 : 1;
    }
    last = index;
    }
    return { score: score - ranges.length * 2, ranges };
    }
    function merge(ranges) {
    const sorted = ranges.slice().sort((a, b) => a[0] - b[0]);
    const out = [];
    for (const range of sorted) {
    const prev = out[out.length - 1];
    if (prev && range[0] <= prev[1]) prev[1] = Math.max(prev[1], range[1]);
    else out.push([range[0], range[1]]);
    }
    return out;
    }
    function fuzzy(query, text) {
    const tokens = fold(query).text.split(/\s+/).filter(Boolean);
    if (!tokens.length) return { score: 0, ranges: [] };
    const target = fold(text);
    let score = 0;
    const found = [];
    for (const token of tokens) {
    const hit = matchToken(token, target.text);
    if (!hit) return null;
    score += hit.score;
    found.push(...hit.ranges);
    }
    if (target.text === fold(query).text.trim()) score += 200;
    const ranges = merge(found).map(([start, end]) => ([target.at[start], end < target.at.length ? target.at[end] : target.length]));
    return { score, ranges: merge(ranges) };
    }
    function filter(items, query, options = {}) {
    const label = options.label ?? ((item) => (item && typeof item === 'object' ? item.label ?? String(item.value ?? '') : String(item ?? '')));
    const out = [];
    (items ?? []).forEach((item, index) => {
    const hit = fuzzy(query, label(item));
    if (hit) out.push({ item, index, score: hit.score, ranges: hit.ranges });
    });
    out.sort((a, b) => b.score - a.score || a.index - b.index);
    return options.limit ? out.slice(0, options.limit) : out;
    }
    function highlight(text, ranges, className = 'ds-listbox__match') {
    const source = String(text ?? '');
    let out = '';
    let last = 0;
    for (const [start, end] of ranges ?? []) {
    if (start < last || end <= start) continue;
    out += `${esc(source.slice(last, start))}<mark class="${esc(className)}">${esc(source.slice(start, end))}</mark>`;
    last = end;
    }
    return out + esc(source.slice(last));
    }
    function entryOf(item, index) {
    if (item === null || typeof item !== 'object') {
    const text = String(item ?? '');
    return { index, value: text, label: text, description: '', meta: '', group: '', disabled: false, item, el: null };
    }
    const value = String(item.value ?? item.label ?? '');
    return {
    index,
    value,
    label: String(item.label ?? value),
    description: item.description ? String(item.description) : '',
    meta: item.meta ? String(item.meta) : '',
    group: item.group ? String(item.group) : '',
    disabled: Boolean(item.disabled),
    className: item.className ? String(item.className) : '',
    pinned: item.pinned ?? false,
    item,
    el: null,
    };
    }
    function parseValue(text, multiple) {
    if (text === null || text === undefined) return undefined;
    if (multiple) {
    try {
    const list = JSON.parse(text);
    if (Array.isArray(list)) return list.map(String);
    } catch {
    }
    return String(text).split(',').map((part) => part.trim()).filter(Boolean);
    }
    return String(text);
    }
    function create(element, options = {}) {
    const existing = controllers.get(element);
    if (existing) {
    if (options.items) existing.setItems(options.items);
    return existing;
    }
    if (!element?.ownerDocument) return null;
    const doc = element.ownerDocument;
    const abort = new AbortController();
    const signal = abort.signal;
    const prefix = options.prefix ?? 'ds-listbox';
    const part = (name) => `${prefix}__${name}`;
    const multiple = options.multiple ?? element.getAttribute('aria-multiselectable') === 'true';
    const focusEl = (options.focus ?? element);
    const standalone = focusEl === element;
    const follow = !multiple && (options.follow ?? standalone);
    const homeEnd = options.homeEnd ?? standalone;
    const typeaheadOn = options.typeahead ?? standalone;
    const spaceChooses = options.space ?? standalone;
    const wrap = options.wrap ?? false;
    const withCheck = options.check ?? prefix === 'ds-listbox';
    const scroller = (options.scroller ?? element);
    const baseId = element.id || `ds-listbox-${(sequence += 1)}`;
    const optionTag = element.localName === 'ul' || element.localName === 'ol' ? 'li' : 'div';
    const virtualFrom = options.virtual === true ? 0 : options.virtual === false ? Infinity : typeof options.virtual === 'number' ? options.virtual : VIRTUAL_FROM;
    let data = null;
    let entries = [];
    let visible = [];
    let positions = new Map();
    let known = new Set();
    let marks = new Map();
    let query = '';
    let active = -1;
    const selected = new Set();
    let windowed = null;
    let destroyed = false;
    let typed = '';
    let typedAt = 0;
    const marked = new WeakSet();
    let hidden = null;
    const ownsActive = new WeakSet();
    const ownsTab = standalone && !element.hasAttribute('tabindex');
    const isDisabled = () => element.getAttribute('aria-disabled') === 'true';
    const syncTab = () => {
    const tab = isDisabled() ? '-1' : '0';
    if (element.getAttribute('tabindex') !== tab) element.setAttribute('tabindex', tab);
    };
    let tabWatch = null;
    if (ownsTab) {
    syncTab();
    own(element, 'attrs', 'tabindex');
    const Observer = doc.defaultView?.MutationObserver;
    if (Observer) {
    tabWatch = new Observer(syncTab);
    tabWatch.observe(element, { attributes: true, attributeFilter: ['aria-disabled'] });
    }
    }
    own(focusEl, 'attrs', 'aria-activedescendant');
    const optionId = (entry) => entry.el?.id || `${baseId}-o${entry.index}`;
    function scan() {
    const optionEls = [...element.querySelectorAll('[role="option"]')];
    entries = optionEls.map((el, index) => {
    if (!el.id) el.id = `${baseId}-o${index}`;
    const name = el.querySelector(`.${part('name')}`);
    const label = (name ?? el).textContent.trim();
    return {
    index,
    value: (el).dataset.value ?? label,
    label,
    description: '',
    meta: '',
    group: '',
    disabled: el.getAttribute('aria-disabled') === 'true',
    pinned: options.pinned?.(el) ?? false,
    item: null,
    el,
    };
    });
    known = new Set(entries.map((entry) => entry.value));
    }
    function selectedFromMarkup() {
    for (const entry of entries) if (entry.el?.getAttribute('aria-selected') === 'true') selected.add(entry.value);
    }
    function setData(items) {
    data = [...(items ?? [])];
    entries = data.map(entryOf);
    known = new Set(entries.map((entry) => entry.value));
    active = -1;
    }
    function show(list) {
    visible = list;
    positions = new Map(list.map((index, at) => [index, at]));
    }
    const positionOf = (index) => positions.get(index) ?? -1;
    function useWindow() {
    return data !== null && visible.length >= virtualFrom;
    }
    function checkHtml() {
    return withCheck ? `<span class="${part('check')}" aria-hidden="true">${icon(check, 16)}</span>` : '';
    }
    function optionHtml(entry) {
    const id = optionId(entry);
    const ranges = marks.get(entry.index) ?? [];
    const name = highlight(entry.label, ranges, part('match'));
    const inner = typeof options.render === 'function'
    ? options.render(entry.item, name)
    : `<span class="${part('name')}">${name}</span>`
    + (entry.description ? `<span class="${part('description')}">${esc(entry.description)}</span>` : '')
    + (entry.meta ? `<span class="${part('meta')}">${esc(entry.meta)}</span>` : '');
    return `<${optionTag} class="${part('option')}${entry.className ? ` ${esc(entry.className)}` : ''}" id="${esc(id)}" data-ds-key="${esc(id)}" data-value="${esc(entry.value)}" role="option" aria-selected="${selected.has(entry.value)}"`
    + `${entry.disabled ? ' aria-disabled="true"' : ''}${entry.index === active ? ' data-active=""' : ''}>${checkHtml()}${inner}</${optionTag}>`;
    }
    function render() {
    if (data === null) return;
    if (useWindow()) {
    const count = visible.length;
    const position = Math.max(0, positionOf(active));
    if (!windowed) {
    element.replaceChildren();
    windowed = createWindow(scroller, { content: element, count, keyboard: false, active: position, estimate: estimateOf(), render: (index) => optionHtml(entries[visible[index]]) });
    } else windowed.set({ count, active: position, forget: true });
    return;
    }
    if (windowed) {
    windowed.destroy();
    windowed = null;
    }
    const groups = new Map();
    const loose = [];
    for (const index of visible) {
    const entry = entries[index];
    if (!entry.group) {
    loose.push(entry);
    continue;
    }
    if (!groups.has(entry.group)) groups.set(entry.group, []);
    groups.get(entry.group).push(entry);
    }
    let html = loose.map(optionHtml).join('');
    let g = 0;
    for (const [group, list] of groups) {
    const head = `${baseId}-g${(g += 1)}`;
    const tag = optionTag === 'li' ? 'ul' : 'div';
    const headTag = optionTag;
    html += `<${tag} class="${part('group')}" role="group" aria-labelledby="${head}" data-ds-key="${head}">`
    + `<${headTag} class="${part('heading')}" id="${head}" role="presentation">${esc(group)}</${headTag}>${list.map(optionHtml).join('')}</${tag}>`;
    }
    morphHtml(element, html);
    const byId = new Map([...element.querySelectorAll('[role="option"]')].map((el) => [el.id, el]));
    for (const index of visible) entries[index].el = byId.get(optionId(entries[index])) ?? null;
    }
    function estimateOf() {
    const probe = doc.createElement(optionTag);
    probe.className = part('option');
    probe.setAttribute('role', 'option');
    const name = doc.createElement('span');
    name.className = part('name');
    name.textContent = 'M';
    probe.append(name);
    element.append(probe);
    const height = probe.getBoundingClientRect().height;
    probe.remove();
    return height || undefined;
    }
    function applyMarkupFilter() {
    for (const entry of entries) {
    const shows = positions.has(entry.index);
    if (entry.el.hidden === shows) entry.el.hidden = !shows;
    const name = entry.el.querySelector(`.${part('name')}`);
    if (!name) continue;
    const ranges = marks.get(entry.index) ?? [];
    if (!ranges.length && !marked.has(name)) continue;
    const nodes = [];
    let last = 0;
    for (const [start, end] of ranges) {
    if (start > last) nodes.push(doc.createTextNode(entry.label.slice(last, start)));
    const mark = doc.createElement('mark');
    mark.className = part('match');
    mark.textContent = entry.label.slice(start, end);
    nodes.push(mark);
    last = end;
    }
    nodes.push(doc.createTextNode(entry.label.slice(last)));
    name.replaceChildren(...nodes);
    if (ranges.length) marked.add(name);
    else marked.delete(name);
    }
    }
    function elementOf(index, reveal) {
    const entry = entries[index];
    if (!entry) return null;
    if (windowed) {
    const position = positionOf(index);
    if (position < 0) return null;
    const row = reveal ? windowed.scrollTo(position) : windowed.row(position);
    entry.el = row;
    return row;
    }
    return entry.el;
    }
    function reveal(el) {
    if (!el || !scroller.isConnected || scroller.clientHeight === 0) return;
    const box = scroller.getBoundingClientRect();
    const rect = el.getBoundingClientRect();
    const style = doc.defaultView.getComputedStyle(scroller);
    const padTop = parseFloat(style.scrollPaddingBlockStart) || 0;
    const padBottom = parseFloat(style.scrollPaddingBlockEnd) || 0;
    if (rect.top < box.top + padTop) scroller.scrollTop -= box.top + padTop - rect.top;
    else if (rect.bottom > box.bottom - padBottom) scroller.scrollTop += rect.bottom - (box.bottom - padBottom);
    }
    function setActive(index, { scroll = true } = {}) {
    const before = entries[active];
    if (before?.el && before.el.isConnected) before.el.removeAttribute('data-active');
    active = index;
    const entry = entries[index];
    if (!entry) {
    active = -1;
    focusEl.removeAttribute('aria-activedescendant');
    if (windowed) windowed.set({});
    return;
    }
    if (windowed) windowed.set({ active: positionOf(index) });
    const el = elementOf(index, scroll);
    if (el) {
    el.setAttribute('data-active', '');
    if (!ownsActive.has(el)) {
    ownsActive.add(el);
    own(el, 'attrs', 'data-active');
    }
    focusEl.setAttribute('aria-activedescendant', el.id);
    if (scroll && !windowed) reveal(el);
    }
    if (scroll && windowed) keepInView(index, 3);
    }
    let viewFrame = 0;
    function keepInView(index, frames) {
    const view = doc.defaultView;
    if (!view) return;
    view.cancelAnimationFrame(viewFrame);
    viewFrame = view.requestAnimationFrame(() => {
    if (destroyed || active !== index || !windowed || !scroller.isConnected || scroller.clientHeight === 0) return;
    const el = entries[index]?.el;
    const box = scroller.getBoundingClientRect();
    const rect = el?.isConnected ? el.getBoundingClientRect() : null;
    if (!rect || (rect.height <= box.height && (rect.top < box.top - 1 || rect.bottom > box.bottom + 1))) {
    windowed.scrollTo(positionOf(index));
    setActive(index, { scroll: false });
    }
    if (frames > 1) keepInView(index, frames - 1);
    });
    }
    const usable = (index) => !entries[index]?.disabled;
    function stepFrom(step) {
    const list = visible.filter(usable);
    if (!list.length) return -1;
    const at = list.indexOf(active);
    if (at < 0) return step > 0 ? list[0] : list[list.length - 1];
    let next = at + step;
    if (wrap && Math.abs(step) === 1) next = (next + list.length) % list.length;
    return list[Math.max(0, Math.min(list.length - 1, next))];
    }
    function edge(which) {
    const list = visible.filter(usable);
    return (which === 'first' ? list[0] : list[list.length - 1]) ?? -1;
    }
    function syncSelected() {
    if (windowed) {
    for (const el of element.querySelectorAll('[role="option"]')) el.setAttribute('aria-selected', String(selected.has( (el).dataset.value)));
    } else {
    for (const entry of data === null ? entries : visible.map((index) => entries[index])) entry.el?.setAttribute('aria-selected', String(selected.has(entry.value)));
    }
    writeHidden();
    }
    function writeHidden() {
    const name = options.name ?? element.getAttribute('data-ds-listbox-name');
    if (!name) return;
    if (!hidden) {
    hidden = doc.createElement('span');
    hidden.hidden = true;
    hidden.setAttribute('data-ds-keep', '');
    element.after(hidden);
    }
    hidden.replaceChildren(...[...selected].map((value) => {
    const input = doc.createElement('input');
    input.type = 'hidden';
    input.name = name;
    input.value = value;
    return input;
    }));
    }
    const value = () => (multiple ? entries.filter((entry) => selected.has(entry.value)).map((entry) => entry.value).concat([...selected].filter((each) => !known.has(each))) : [...selected][0] ?? null);
    function changed(entry, isSelected) {
    emit(element, 'change', { value: value(), item: entry ? entry.item ?? entry.value : null, selected: isSelected });
    }
    function choose(index, { toggle = multiple } = {}) {
    const entry = entries[index];
    if (!entry || entry.disabled || isDisabled()) return false;
    let isSelected = true;
    if (multiple && toggle) {
    isSelected = !selected.has(entry.value);
    if (isSelected) selected.add(entry.value);
    else selected.delete(entry.value);
    } else if (multiple) {
    selected.add(entry.value);
    } else {
    if (selected.size === 1 && selected.has(entry.value)) return true;
    selected.clear();
    selected.add(entry.value);
    }
    syncSelected();
    changed(entry, isSelected);
    return true;
    }
    function selectAll() {
    const list = visible.filter(usable);
    const all = list.every((index) => selected.has(entries[index].value));
    for (const index of list) {
    if (all) selected.delete(entries[index].value);
    else selected.add(entries[index].value);
    }
    syncSelected();
    changed(null, !all);
    }
    function runFilter(text, how = {}) {
    query = String(text ?? '');
    marks = new Map();
    const pinned = entries.filter((entry) => entry.pinned);
    const pool = entries.filter((entry) => !entry.pinned);
    let list;
    if (!query.trim()) list = pool.map((entry) => entry.index);
    else if (how.keep) {
    list = pool.map((entry) => entry.index);
    for (const entry of pool) {
    const hit = fuzzy(query, entry.label);
    if (hit?.ranges.length) marks.set(entry.index, hit.ranges);
    }
    } else {
    const hits = filter(pool, query, { label: (entry) => entry.label });
    if (data === null) hits.sort((a, b) => a.index - b.index);
    list = hits.map((hit) => hit.item.index);
    for (const hit of hits) marks.set(hit.item.index, hit.ranges);
    }
    const matches = list.length;
    for (const entry of pinned) if (typeof entry.pinned !== 'function' || entry.pinned(query, matches)) list.push(entry.index);
    show(list);
    if (data === null) applyMarkupFilter();
    else render();
    if (!positions.has(active)) setActive(-1);
    return visible.length;
    }
    function key(event) {
    if (event.defaultPrevented || event.isComposing || isDisabled()) return false;
    const mod = event.ctrlKey || event.metaKey;
    const k = event.key;
    let target = -1;
    if (k === 'ArrowDown' || k === 'ArrowUp') {
    if (event.altKey) return false;
    target = stepFrom(k === 'ArrowDown' ? 1 : -1);
    } else if ((k === 'Home' || k === 'End') && homeEnd) target = edge(k === 'Home' ? 'first' : 'last');
    else if (k === 'PageDown' || k === 'PageUp') target = stepFrom(k === 'PageDown' ? PAGE : -PAGE);
    else if (k === 'Enter' && !mod) {
    if (active < 0) return false;
    choose(active);
    return true;
    } else if (k === ' ' && spaceChooses && !mod) {
    if (active < 0) target = stepFrom(1);
    else {
    choose(active);
    return true;
    }
    } else if (multiple && mod && !event.shiftKey && (k === 'a' || k === 'A') && standalone) {
    selectAll();
    return true;
    } else if (typeaheadOn && k.length === 1 && !mod && !event.altKey) {
    return typeahead(k);
    } else return false;
    if (target < 0) return k === 'ArrowDown' || k === 'ArrowUp';
    setActive(target);
    if (follow) choose(target);
    else if (multiple && event.shiftKey && (k === 'ArrowDown' || k === 'ArrowUp')) choose(target);
    return true;
    }
    function typeahead(char) {
    if (char === ' ' && !typed) return false;
    const now = Date.now();
    if (now - typedAt > motionDuration(element, '--ds-delay-tooltip')) typed = '';
    typedAt = now;
    typed += char.toLowerCase();
    const list = visible.filter(usable).map((index) => entries[index]);
    const match = typeaheadMatch(list, entries[active] ?? null, typed, (entry) => entry.label.toLowerCase());
    if (!match) return true;
    setActive(match.index);
    if (follow) choose(match.index);
    return true;
    }
    const indexOfNode = (node) => {
    const el = (node)?.closest?.('[role="option"]');
    if (!el || !element.contains(el)) return -1;
    const entry = entries.find((each) => each.el === el || (el.id && optionId(each) === el.id));
    return entry ? entry.index : -1;
    };
    element.addEventListener('click', ( event) => {
    const index = indexOfNode(event.target);
    if (index < 0 || !usable(index) || isDisabled()) return;
    setActive(index, { scroll: false });
    if (multiple && event.shiftKey && standalone) choose(index, { toggle: false });
    else choose(index);
    if (!standalone) return;
    if (doc.activeElement !== element) element.focus({ preventScroll: true });
    }, { signal });
    if (!standalone) {
    element.addEventListener('pointermove', (event) => {
    if (event.pointerType !== 'mouse') return;
    const index = indexOfNode(event.target);
    if (index >= 0 && index !== active && usable(index)) setActive(index, { scroll: false });
    }, { signal });
    element.addEventListener('mousedown', (event) => event.preventDefault(), { signal });
    } else {
    element.addEventListener('keydown', (event) => {
    if (key(event)) event.preventDefault();
    }, { signal });
    element.addEventListener('focus', () => {
    if (active >= 0 && positions.has(active)) {
    setActive(active);
    return;
    }
    const first = visible.find((index) => selected.has(entries[index].value) && usable(index)) ?? visible.find(usable);
    if (first !== undefined) setActive(first);
    }, { signal });
    }
    const edges = prefix === 'ds-listbox' && !element.classList.contains('ds-listbox--plain') && !element.hasAttribute('data-ds-edges') ? scrollEdges(scroller, { axis: 'block' }) : null;
    if (Array.isArray(options.items)) setData(options.items);
    else scan();
    const initial = options.value !== undefined ? options.value : parseValue(element.getAttribute('data-ds-value'), multiple);
    if (initial !== undefined && initial !== null) for (const each of multiple ? [].concat(initial) : [initial]) selected.add(String(each));
    else if (data === null) selectedFromMarkup();
    if (!multiple && selected.size > 1) {
    const first = [...selected][0];
    selected.clear();
    selected.add(first);
    }
    show(entries.map((entry) => entry.index));
    render();
    syncSelected();
    const controller = {
    element,
    get: value,
    set(next) {
    selected.clear();
    if (next !== null && next !== undefined) for (const each of multiple ? [].concat(next) : [next]) selected.add(String(each));
    syncSelected();
    },
    on(type, fn) {
    const listener = ( event) => {
    if (event.target === element) fn(event.detail);
    };
    element.addEventListener(`ds:${type}`, listener);
    return () => element.removeEventListener(`ds:${type}`, listener);
    },
    setItems(items, how = {}) {
    setData(items);
    return runFilter(how.query ?? query, how);
    },
    filter: runFilter,
    shown: () => visible.map((index) => entries[index]),
    entries: () => entries.slice(),
    active: () => entries[active] ?? null,
    activate(which) {
    if (which === null) return setActive(-1);
    if (which === 'first' || which === 'last') return setActive(edge(which));
    const entry = entries.find((each) => each.value === String(which));
    return setActive(entry && positions.has(entry.index) ? entry.index : -1);
    },
    move(step) {
    const target = stepFrom(step);
    if (target >= 0) setActive(target);
    return entries[active] ?? null;
    },
    pick(which) {
    const entry = which === undefined ? entries[active] : entries.find((each) => each.value === String(which));
    return entry ? choose(entry.index) : false;
    },
    key,
    refresh() {
    if (ownsTab) syncTab();
    if (data !== null) return render();
    scan();
    runFilter(query);
    syncSelected();
    },
    destroy() {
    if (destroyed) return;
    destroyed = true;
    doc.defaultView?.cancelAnimationFrame(viewFrame);
    abort.abort();
    tabWatch?.disconnect();
    controllers.delete(element);
    windowed?.destroy();
    edges?.destroy();
    hidden?.remove();
    focusEl.removeAttribute('aria-activedescendant');
    disown(focusEl, 'attrs', 'aria-activedescendant');
    if (entries[active]?.el) entries[active].el.removeAttribute('data-active');
    if (ownsTab) {
    element.removeAttribute('tabindex');
    disown(element, 'attrs', 'tabindex');
    }
    },
    };
    controllers.set(element, controller);
    return controller;
    }
    define('listbox', {
    attribute: 'data-ds-listbox',
    init: (element) => create(element, {
    virtual: element.hasAttribute('data-ds-listbox-virtual') ? element.getAttribute('data-ds-listbox-virtual') !== 'false' : undefined,
    }),
    });

    return Object.freeze({
      VIRTUAL_FROM: VIRTUAL_FROM,
      fuzzy: fuzzy,
      filter: filter,
      highlight: highlight,
      create: create
    });
  });

  add('combobox', ['create', 'open', 'close'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const motionDuration = DS.motion.motionDuration;
    const openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close;
    const announce = DS.a11y.announce;
    const plural = DS.format.plural;
    const icon = DS.icons.icon, iconElement = DS.icons.iconElement, closeGlyph = DS.icons.close, plus = DS.icons.plus;
    const createList = DS.listbox.create;
    const CREATE = '\u0000ds-create';
    const controllers = new WeakMap();
    let sequence = 0;
    const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
    const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ESCAPES[char]);
    function itemsOf(list) {
    return [...list.querySelectorAll('.ds-combo__option')].filter((option) => !option.classList.contains('ds-combo__option--create')).map((option) => {
    const name = option.querySelector('.ds-combo__name');
    const label = (name ?? option).textContent.trim();
    const heading = option.closest('.ds-combo__group')?.querySelector('.ds-combo__heading');
    return {
    value: (option).dataset.value ?? label,
    label,
    description: option.querySelector('.ds-combo__description')?.textContent.trim() ?? '',
    meta: option.querySelector('.ds-combo__meta')?.textContent.trim() ?? '',
    group: heading?.textContent.trim() ?? '',
    disabled: option.getAttribute('aria-disabled') === 'true',
    selected: option.getAttribute('aria-selected') === 'true',
    };
    });
    }
    function numberOf(element, name, fallback) {
    const value = Number(element.getAttribute(name));
    return element.hasAttribute(name) && Number.isFinite(value) ? value : fallback;
    }
    function create(element, options = {}) {
    const existing = controllers.get(element);
    if (existing) {
    if (options.items) existing.setItems(options.items);
    return existing;
    }
    const input = (element?.querySelector?.('.ds-combo__input'));
    const popup = (element?.querySelector?.('.ds-combo__popup'));
    const list = (popup?.querySelector('.ds-combo__list'));
    if (!input || !popup || !list) return null;
    const doc = element.ownerDocument;
    const abort = new AbortController();
    const signal = abort.signal;
    const select = element.classList.contains('ds-combo--select');
    const multiple = !select && (options.multiple ?? (element.classList.contains('ds-combo--multi') || list.getAttribute('aria-multiselectable') === 'true'));
    const creatable = !select && (options.creatable ?? element.hasAttribute('data-ds-combobox-creatable'));
    const source = typeof options.source === 'function' ? options.source : null;
    const minLength = options.minLength ?? numberOf(element, 'data-ds-combobox-min', 1);
    const toggle = element.querySelector('.ds-combo__toggle');
    const clear = (element.querySelector('.ds-combo__clear'));
    const name = options.name ?? element.getAttribute('data-ds-combobox-name');
    if (!list.id) list.id = `ds-combo-list-${(sequence += 1)}`;
    if (!input.hasAttribute('aria-controls')) input.setAttribute('aria-controls', list.id);
    if (!select && !input.hasAttribute('aria-autocomplete')) input.setAttribute('aria-autocomplete', 'list');
    if (!select) input.setAttribute('autocomplete', 'off');
    if (multiple) list.setAttribute('aria-multiselectable', 'true');
    let status = popup.querySelector('.ds-combo__status');
    if (!status && !select) {
    status = doc.createElement('p');
    status.className = 'ds-combo__status';
    status.setAttribute('role', 'status');
    popup.append(status);
    }
    const markupItems = itemsOf(list);
    let fromMarkup = !options.items && !source;
    let base = options.items ? [...options.items] : markupItems;
    const labels = new Map();
    const remember = (items) => {
    for (const item of items) {
    if (item && typeof item === 'object') labels.set(String(item.value ?? item.label), String(item.label ?? item.value));
    else labels.set(String(item), String(item));
    }
    };
    remember(base);
    const labelOf = (value) => labels.get(value) ?? value;
    let query = '';
    let searching = 0;
    let timer = 0;
    let pending = null;
    let resultsFor = null;
    let destroyed = false;
    const createItem = {
    value: CREATE,
    label: '',
    className: 'ds-combo__option--create',
    pinned: (text) => creatable && text.trim() !== '' && !hasLabel(text),
    };
    const hasLabel = (text) => {
    const wanted = text.trim().toLowerCase();
    return engineEntries().some((entry) => entry.value !== CREATE && entry.label.toLowerCase() === wanted);
    };
    const withCreate = (items) => (creatable ? [...items, createItem] : items);
    const optionTag = list.localName === 'ul' || list.localName === 'ol' ? 'li' : 'div';
    let createEl = fromMarkup && creatable ? list.querySelector('.ds-combo__option--create') : null;
    const madeCreate = fromMarkup && creatable && !createEl;
    if (madeCreate) {
    createEl = doc.createElement(optionTag);
    createEl.className = 'ds-combo__option ds-combo__option--create';
    createEl.setAttribute('role', 'option');
    createEl.setAttribute('aria-selected', 'false');
    createEl.hidden = true;
    const createName = doc.createElement('span');
    createName.className = 'ds-combo__name';
    createEl.append(iconElement(plus, 20, '', doc), createName);
    list.append(createEl);
    }
    if (createEl) createEl.setAttribute('data-value', CREATE);
    const isCreate = (el) => el.classList.contains('ds-combo__option--create');
    const engine = createList(list, {
    ...(fromMarkup ? {} : { items: withCreate(base) }),
    pinned: (el) => (isCreate(el) ? createItem.pinned : false),
    multiple,
    focus: input,
    prefix: 'ds-combo',
    scroller: list,
    wrap: !select,
    follow: false,
    homeEnd: select,
    typeahead: select,
    space: false,
    virtual: options.virtual ?? (element.hasAttribute('data-ds-combobox-virtual') ? element.getAttribute('data-ds-combobox-virtual') !== 'false' : undefined),
    render: (item, nameHtml) => (item?.value === CREATE
    ? `${icon(plus, 20)}<span class="ds-combo__name">${esc(t('createOption', { text: query.trim() }))}</span>`
    : `<span class="ds-combo__name">${nameHtml}</span>${item?.description ? `<span class="ds-combo__description">${esc(item.description)}</span>` : ''}${item?.meta ? `<span class="ds-combo__meta">${esc(item.meta)}</span>` : ''}`),
    });
    const engineEntries = () => engine.entries();
    function filterNow(text) {
    query = text;
    if (fromMarkup && createEl) {
    const nameEl = createEl.querySelector('.ds-combo__name');
    if (nameEl) nameEl.textContent = t('createOption', { text: text.trim() });
    }
    engine.filter(text);
    }
    if (fromMarkup) filterNow('');
    let value = multiple ? [] : null;
    const initial = options.value !== undefined ? options.value : element.hasAttribute('data-ds-value') ? (multiple ? parseList(element.getAttribute('data-ds-value')) : element.getAttribute('data-ds-value')) : undefined;
    if (initial !== undefined) value = multiple ? [].concat(initial ?? []).map(String) : initial === null ? null : String(initial);
    else {
    const chosen = markupItems.filter((item) => item.selected).map((item) => item.value);
    value = multiple ? chosen : chosen[0] ?? null;
    }
    engine.set(value);
    const isOpen = () => input.getAttribute('aria-expanded') === 'true';
    const disabled = () => input.disabled || input.getAttribute('aria-disabled') === 'true' || input.readOnly;
    function renderChips() {
    if (!multiple) return;
    const chips = [...element.querySelectorAll(':scope > .ds-chip[data-ds-combobox-chip]')];
    const wanted = (value);
    for (const chip of chips) if (!wanted.includes(chip.getAttribute('data-value'))) chip.remove();
    for (const each of wanted) {
    if (element.querySelector(`:scope > .ds-chip[data-ds-combobox-chip][data-value="${CSS.escape(each)}"]`)) continue;
    const chip = doc.createElement('button');
    chip.type = 'button';
    chip.className = 'ds-chip ds-chip--input';
    chip.setAttribute('data-ds-combobox-chip', '');
    chip.setAttribute('data-value', each);
    chip.tabIndex = -1;
    chip.disabled = input.disabled;
    const body = doc.createElement('span');
    body.className = 'ds-chip__body';
    const label = doc.createElement('span');
    label.className = 'ds-chip__label';
    const sr = doc.createElement('span');
    sr.className = 'ds-sr';
    sr.textContent = `${t('remove')} `;
    label.append(sr, doc.createTextNode(labelOf(each)));
    body.append(label, iconElement(closeGlyph, 18, 'ds-chip__trailing', doc));
    chip.append(body);
    input.before(chip);
    }
    }
    function writeHidden() {
    if (!name) return;
    let holder = element.querySelector(':scope > [data-ds-combobox-values]');
    if (!holder) {
    holder = doc.createElement('span');
    holder.hidden = true;
    holder.setAttribute('data-ds-combobox-values', '');
    element.append(holder);
    }
    holder.replaceChildren(...[].concat(value ?? []).map((each) => {
    const field = doc.createElement('input');
    field.type = 'hidden';
    field.name = name;
    field.value = each;
    return field;
    }));
    }
    function syncClear() {
    if (!clear) return;
    const empty = multiple ? !( (value).length) && !input.value : !input.value && value === null;
    clear.hidden = empty || disabled();
    }
    function showValue() {
    if (select) {
    const shown = element.querySelector('.ds-combo__value');
    if (shown) shown.textContent = value === null ? '' : labelOf( (value));
    const metaEl = input.querySelector('.ds-combo__meta');
    if (metaEl) {
    const item = base.find((each) => each && typeof each === 'object' && String(each.value ?? each.label) === value);
    metaEl.textContent = item?.meta ?? '';
    }
    } else if (!multiple) input.value = value === null ? '' : labelOf( (value));
    renderChips();
    writeHidden();
    syncClear();
    }
    function changed(item) {
    emit(element, 'change', { value: multiple ? [...value] : value, item: item ?? null });
    }
    function setStatus(text, busy = false) {
    list.setAttribute('aria-busy', String(busy));
    if (!busy) list.removeAttribute('aria-busy');
    if (!status) return;
    const parts = [];
    if (busy) {
    const spinner = doc.createElement('span');
    spinner.className = 'ds-spinner ds-spinner--ink ds-sm';
    spinner.setAttribute('aria-hidden', 'true');
    parts.push(spinner);
    }
    parts.push(doc.createTextNode(text));
    status.replaceChildren(...parts);
    }
    function countText(count) {
    if (!count) return t('noResults');
    return plural(count, { one: () => t('resultsOne', { count }), other: ({ formatted }) => t('resultsOther', { count: formatted }) });
    }
    function settle() {
    const found = engine.shown().filter((entry) => entry.value !== CREATE).length;
    list.hidden = engine.shown().length === 0;
    if (searching) return;
    setStatus(source && query.trim().length < minLength ? t('typeToSearch') : countText(found));
    }
    function run() {
    query = select ? '' : input.value;
    if (!source) {
    filterNow(query);
    settle();
    return;
    }
    clearTimeout(timer);
    pending?.abort();
    if (query.trim().length < minLength) {
    searching = 0;
    resultsFor = null;
    engine.setItems(withCreate([]));
    settle();
    return;
    }
    const wait = options.debounce ?? numberOf(element, 'data-ds-combobox-debounce', motionDuration(element, '--ds-delay-loader'));
    timer = setTimeout(search, wait);
    }
    function search() {
    const asked = query;
    if (!emit(element, 'search', { query: asked }, { cancelable: true })) return;
    const controller = new AbortController();
    pending = controller;
    searching += 1;
    setStatus(t('loading'), true);
    Promise.resolve()
    .then(() => source(asked, { signal: controller.signal }))
    .then((items) => {
    if (controller.signal.aborted || destroyed) return;
    base = [...(items ?? [])];
    remember(base);
    searching = 0;
    resultsFor = asked;
    engine.setItems(withCreate(base), { keep: true, query: asked });
    settle();
    }, (error) => {
    if (controller.signal.aborted || destroyed || error?.name === 'AbortError') return;
    searching = 0;
    resultsFor = null;
    engine.setItems(withCreate([]));
    list.hidden = true;
    setStatus(t('actionFailed'));
    });
    }
    function openList({ all = false } = {}) {
    if (isOpen() || disabled()) return;
    if (!source) filterNow(select || all ? '' : input.value);
    else if (!select) {
    const wanted = input.value;
    if (wanted.trim().length >= minLength && wanted !== resultsFor && !(searching && wanted === query)) {
    clearTimeout(timer);
    pending?.abort();
    query = wanted;
    search();
    }
    }
    settle();
    openOverlay(popup, { opener: input });
    }
    function closeList() {
    engine.activate(null);
    if (isOpen()) closeOverlay(popup);
    }
    popup.addEventListener('ds:close', () => engine.activate(null), { signal });
    list.addEventListener('ds:change', ( event) => {
    event.stopPropagation();
    const detail = event.detail;
    const item = typeof detail.item === 'string' && fromMarkup ? base.find((each) => each.value === detail.item) ?? detail.item : detail.item;
    if (item === CREATE || (item && typeof item === 'object' && item.value === CREATE)) {
    engine.set(value);
    createFrom(query.trim());
    return;
    }
    if (multiple) {
    value = (detail.value).filter((each) => each !== CREATE);
    const label = labelOf(String(item?.value ?? item?.label ?? item));
    announce(t(detail.selected ? 'added' : 'removed', { label }));
    if (input.value) {
    input.value = '';
    run();
    }
    showValue();
    changed(item);
    return;
    }
    value = detail.value;
    showValue();
    closeList();
    if (source) {
    clearTimeout(timer);
    pending?.abort();
    searching = 0;
    query = input.value;
    resultsFor = query;
    settle();
    } else if (!select) run();
    changed(item);
    }, { signal });
    function createFrom(text) {
    if (!text) return;
    if (!emit(element, 'create', { text }, { cancelable: true })) return;
    const item = { value: text, label: text };
    base = [...base, item];
    remember([item]);
    if (fromMarkup) {
    const option = doc.createElement(optionTag);
    option.className = 'ds-combo__option';
    option.setAttribute('role', 'option');
    option.setAttribute('aria-selected', 'false');
    option.setAttribute('data-value', text);
    const nameEl = doc.createElement('span');
    nameEl.className = 'ds-combo__name';
    nameEl.textContent = text;
    option.append(nameEl);
    if (createEl?.parentElement === list) createEl.before(option);
    else list.append(option);
    engine.refresh();
    } else engine.setItems(withCreate(base));
    if (multiple) {
    value = [...value, text];
    announce(t('added', { label: text }));
    input.value = '';
    engine.set(value);
    run();
    } else {
    value = text;
    engine.set(value);
    closeList();
    }
    showValue();
    changed(item);
    }
    function removeValue(each) {
    if (!multiple) return;
    value = value.filter((other) => other !== each);
    engine.set(value);
    announce(t('removed', { label: labelOf(each) }));
    showValue();
    changed(null);
    }
    input.addEventListener('input', () => {
    if (select) return;
    if (!multiple && input.value === '' && value !== null) {
    value = null;
    engine.set(null);
    changed(null);
    }
    run();
    syncClear();
    if (!isOpen()) openOverlay(popup, { opener: input });
    }, { signal });
    input.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.isComposing || disabled()) return;
    const k = event.key;
    if (!isOpen()) {
    if (k === 'ArrowDown' || k === 'ArrowUp' || (select && (k === 'Enter' || k === ' '))) {
    event.preventDefault();
    openList();
    if (event.altKey && !select) return;
    const chosen = [].concat(value ?? [])[0];
    if (chosen !== undefined && engine.shown().some((entry) => entry.value === chosen)) engine.activate(chosen);
    else engine.activate(k === 'ArrowUp' ? 'last' : 'first');
    return;
    }
    if (select && (k === 'Home' || k === 'End')) {
    event.preventDefault();
    openList();
    engine.activate(k === 'Home' ? 'first' : 'last');
    return;
    }
    if (select && k.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
    openList();
    if (engine.key(event)) event.preventDefault();
    return;
    }
    if (multiple && k === 'Backspace' && !input.value && value.length) {
    event.preventDefault();
    removeValue(value[value.length - 1]);
    }
    return;
    }
    if (select && (k === ' ' || k === 'Enter')) {
    event.preventDefault();
    if (!engine.pick()) closeList();
    return;
    }
    if (k === 'Tab') {
    if (select && engine.active()) engine.pick();
    closeList();
    return;
    }
    if (multiple && k === 'Backspace' && !input.value && value.length) {
    event.preventDefault();
    removeValue(value[value.length - 1]);
    return;
    }
    if (event.altKey && k === 'ArrowUp') {
    event.preventDefault();
    closeList();
    return;
    }
    if (engine.key(event)) event.preventDefault();
    }, { signal });
    input.addEventListener('blur', () => {
    closeList();
    if (!multiple && !select && value !== null && input.value !== labelOf(value)) input.value = labelOf(value);
    syncClear();
    }, { signal });
    if (!select) {
    input.addEventListener('click', () => {
    if (!isOpen()) openList();
    }, { signal });
    } else {
    input.addEventListener('click', ( event) => {
    if (event.detail > 1 || disabled()) return;
    if (isOpen()) closeList();
    else {
    openList();
    if (value !== null) engine.activate(value);
    }
    }, { signal });
    }
    for (const part of [toggle, clear, popup]) part?.addEventListener('mousedown', (event) => event.preventDefault(), { signal });
    toggle?.addEventListener('click', () => {
    if (disabled()) return;
    input.focus({ preventScroll: true });
    if (isOpen()) closeList();
    else {
    openList({ all: !multiple && value !== null && input.value === labelOf( (value)) });
    const chosen = [].concat(value ?? [])[0];
    if (chosen !== undefined) engine.activate(chosen);
    }
    }, { signal });
    clear?.addEventListener('click', () => {
    const had = multiple ? value.length > 0 : value !== null;
    input.value = '';
    value = multiple ? [] : null;
    engine.set(value);
    run();
    showValue();
    input.focus({ preventScroll: true });
    if (had) changed(null);
    }, { signal });
    if (multiple) {
    element.addEventListener('click', ( event) => {
    const chip = (event.target)?.closest?.('.ds-chip[data-ds-combobox-chip]');
    if (!chip || chip.parentElement !== element || disabled()) return;
    removeValue(chip.getAttribute('data-value'));
    input.focus({ preventScroll: true });
    }, { signal });
    element.addEventListener('mousedown', (event) => {
    if (event.target !== element) return;
    event.preventDefault();
    input.focus();
    }, { signal });
    }
    showValue();
    settle();
    const controller = {
    element,
    get: () => (multiple ? [...value] : value),
    set(next) {
    value = multiple ? [].concat(next ?? []).map(String) : next === null || next === undefined ? null : String(next);
    engine.set(value);
    showValue();
    },
    on(type, fn) {
    const listener = ( event) => {
    if (event.target === element) fn(event.detail);
    };
    element.addEventListener(`ds:${type}`, listener);
    return () => element.removeEventListener(`ds:${type}`, listener);
    },
    setItems(items) {
    fromMarkup = false;
    base = [...items];
    remember(base);
    engine.setItems(withCreate(base));
    run();
    },
    open: openList,
    close: closeList,
    list: engine,
    destroy() {
    if (destroyed) return;
    destroyed = true;
    clearTimeout(timer);
    pending?.abort();
    abort.abort();
    engine.destroy();
    if (madeCreate) createEl?.remove();
    controllers.delete(element);
    },
    };
    controllers.set(element, controller);
    return controller;
    }
    function parseList(text) {
    try {
    const list = JSON.parse(text);
    if (Array.isArray(list)) return list.map(String);
    } catch {
    }
    return String(text ?? '').split(',').map((part) => part.trim()).filter(Boolean);
    }
    function open(element) {
    const control = controllers.get(element);
    if (control) {
    control.open();
    return Promise.resolve();
    }
    const popup = element?.querySelector('.ds-combo__popup');
    return popup ? openOverlay(popup, { opener: element.querySelector('.ds-combo__input') }) : Promise.resolve();
    }
    function close(element) {
    const popup = element?.querySelector('.ds-combo__popup');
    controllers.get(element)?.list.activate(null);
    return popup ? closeOverlay(popup) : Promise.resolve();
    }
    define('combobox', {
    attribute: 'data-ds-combobox',
    init: (element) => create(element),
    });

    return Object.freeze({
      create: create,
      open: open,
      close: close
    });
  });

  add('menu', ['open', 'openAt', 'close', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const motionDuration = DS.motion.motionDuration, originFrom = DS.motion.originFrom, settled = DS.motion.settled;
    const computePlacement = DS.position.computePlacement, place = DS.position.place, pxOf = DS.position.pxOf, releaseAnchor = DS.position.releaseAnchor, round = DS.position.round, viewportBounds = DS.position.viewportBounds;
    const announce = DS.a11y.announce;
    const internals = DS.overlay.internals, openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close, typeAhead = DS.overlay.typeAhead;
    const { overlays, stack, viewOf, canFocus, activeIn, inside, closeFrom, arrowTarget, placeMenu, controllerOf, clock, topOpen, flushStyle, own, disown, composedContains } = internals;
    function menuItems(menu) {
    return [...menu.querySelectorAll('.ds-menu__item')].filter((item) => item.closest('.ds-menu') === menu && canFocus(item));
    }
    const enabled = (item) => item.getAttribute('aria-disabled') !== 'true';
    function firstItem(menu) {
    return menuItems(menu).find(enabled) ?? null;
    }
    function searchOf(menu) {
    const field = menu.querySelector('.ds-menu__search input');
    return field && field.closest('.ds-menu') === menu ? (field) : null;
    }
    function subOf(item) {
    const popup = item?.getAttribute?.('aria-haspopup');
    if (popup !== 'menu' && popup !== 'true') return null;
    const id = item.getAttribute('aria-controls');
    if (!id) return null;
    const scope = item.getRootNode?.() ?? item.ownerDocument;
    const sub = scope.getElementById?.(id) ?? item.ownerDocument.getElementById(id);
    return sub?.matches('.ds-menu') ? sub : null;
    }
    const custom = new WeakMap();
    const unexpanded = new WeakMap();
    const focusMode = new WeakMap();
    function rootOf(menu) {
    let at = menu;
    for (let guard = 0; guard < 16; guard += 1) {
    const opener = overlays.get(at)?.opener;
    const parent = opener?.closest?.('.ds-menu');
    if (!parent || !custom.has(at) || custom.get(at)?.kind !== 'sub') return at;
    at = parent;
    }
    return at;
    }
    const isOpen = (element) => overlays.get(element)?.phase === 'open';
    const openSubsOf = (menu) => stack.filter((other) => custom.get(other)?.kind === 'sub' && isOpen(other) && overlays.get(other)?.opener?.closest?.('.ds-menu') === menu);
    const isTop = (element) => {
    try {
    return element.matches(':popover-open');
    } catch {
    return false;
    }
    };
    function placeAtPoint(menu, point, options) {
    const style = (menu).style;
    style.removeProperty('max-height');
    const size = { width: (menu).offsetWidth, height: (menu).offsetHeight };
    const rtl = viewOf(menu).getComputedStyle?.(menu).direction === 'rtl';
    const result = computePlacement({ left: point.x, right: point.x, top: point.y, bottom: point.y }, size, viewportBounds(menu), { ...options, rtl });
    if (size.height > result.room) style.setProperty('max-height', `${round(result.room)}px`);
    if (result.side === 'bottom') menu.removeAttribute('data-placement');
    else menu.setAttribute('data-placement', result.side);
    return result;
    }
    function placeCustom(menu) {
    const how = custom.get(menu);
    if (!how) return;
    const style = (menu).style;
    if (menu.hasAttribute('data-anchored')) {
    menu.removeAttribute('data-anchored');
    style.removeProperty('position-anchor');
    const state = overlays.get(menu);
    if (state?.anchor) {
    releaseAnchor(state.anchor);
    state.anchor = null;
    }
    }
    if (!isTop(menu) && typeof (menu).showPopover === 'function') {
    menu.setAttribute('popover', 'manual');
    try {
    (menu).showPopover();
    } catch {
    }
    }
    const padding = pxOf(menu, '--ds-space-2', 8);
    const result = how.kind === 'sub'
    ? place( (menu), (how.anchor), { side: 'end', align: 'start', offset: 0, padding, flip: 'room', size: false })
    : placeAtPoint(menu, (how.anchor), { side: 'bottom', align: 'start', offset: 0, padding, flip: 'room', crossFlip: true });
    if (!result) return;
    const lift = how.kind === 'sub' ? parseFloat(viewOf(menu).getComputedStyle(menu).paddingTop) || 0 : 0;
    const height = (menu).offsetHeight;
    const bottom = (menu.ownerDocument.documentElement.clientHeight || viewOf(menu).innerHeight) - padding;
    const y = Math.max(padding, Math.min(result.y - lift, bottom - height));
    style.setProperty('--ds-menu-x', `${round(result.x)}px`);
    style.setProperty('--ds-menu-y', `${round(y)}px`);
    if (result.side === 'bottom') menu.removeAttribute('data-placement');
    }
    function enterCustom(menu) {
    const how = custom.get(menu);
    if (!how) return;
    const style = (menu).style;
    style.setProperty('transition', 'none');
    style.setProperty('scale', 'none');
    menu.setAttribute('data-state', 'closed');
    placeCustom(menu);
    originFrom(how.kind === 'sub' ? (how.anchor) : how.anchor, menu);
    style.removeProperty('scale');
    flushStyle(menu);
    style.removeProperty('transition');
    menu.setAttribute('data-state', 'open');
    }
    const listening = new WeakSet();
    function listen(doc) {
    if (listening.has(doc)) return;
    listening.add(doc);
    doc.addEventListener('keydown', ( event) => {
    if (event.key !== 'Tab' || event.isComposing || event.defaultPrevented) return;
    const top = topOpen();
    if (!top || overlays.get(top)?.kind !== 'menu' || !top.contains(activeIn(top))) return;
    const root = rootOf(top);
    const opener = overlays.get(root)?.opener;
    if (canFocus(opener)) opener.focus({ preventScroll: true });
    closeChain(top, 'tab');
    }, true);
    doc.addEventListener('pointerdown', ( event) => {
    for (const element of [...stack].reverse()) {
    if (custom.get(element)?.kind !== 'point' || !isOpen(element) || inside(element, event)) continue;
    if (stack.slice(stack.indexOf(element) + 1).some((other) => isOpen(other) && inside(other, event))) continue;
    closeFrom(element, 'outside');
    }
    }, true);
    doc.addEventListener('ds:close', (event) => {
    const menu = (event.target);
    if (!menu?.matches?.('.ds-menu')) return;
    for (const sub of openSubsOf(menu)) closeFrom(sub, 'parent');
    const row = unexpanded.get(menu);
    if (row) {
    unexpanded.delete(menu);
    row.removeAttribute('aria-expanded');
    }
    const state = overlays.get(menu);
    state?.closing?.then(() => {
    if (state.phase === 'closed') custom.delete(menu);
    });
    });
    const view = doc.defaultView ?? globalThis;
    let frame = 0;
    const follow = ( event) => {
    if (event?.type === 'scroll' && (event.target)?.closest?.('.ds-menu')) return;
    if (frame || !stack.some((element) => overlays.get(element)?.kind === 'menu')) return;
    const run = () => {
    frame = 0;
    for (const element of stack) {
    const state = overlays.get(element);
    if (state?.kind !== 'menu' || state.phase === 'closed') continue;
    if (custom.has(element)) {
    if (custom.get(element)?.kind === 'sub') placeCustom(element);
    } else if (!element.hasAttribute('data-anchored')) placeMenu(element, state.opener);
    }
    };
    frame = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(run) : 0;
    if (!frame) run();
    };
    doc.addEventListener('scroll', follow, { capture: true, passive: true });
    view.addEventListener?.('resize', follow);
    }
    function closeChain(menu, cause) {
    const chain = [];
    for (let at = menu; at; ) {
    chain.push(at);
    const how = custom.get(at);
    const opener = overlays.get(at)?.opener;
    at = how?.kind === 'sub' ? opener?.closest?.('.ds-menu') ?? null : null;
    if (chain.length > 16) break;
    }
    for (const element of chain) closeFrom(element, cause);
    }
    function flipCheck(menu, item) {
    if (!menu.hasAttribute('data-ds-menu-checks')) return;
    const role = item.getAttribute('role');
    if (role !== 'menuitemcheckbox' && role !== 'menuitemradio') return;
    const was = item.getAttribute('aria-checked') === 'true';
    if (role === 'menuitemradio' && was) return;
    const checked = role === 'menuitemradio' || !was;
    if (role === 'menuitemradio') {
    const group = item.closest('[role="group"]') ?? menu;
    for (const other of group.querySelectorAll('[role="menuitemradio"]')) {
    if (other.closest('.ds-menu') === menu) other.setAttribute('aria-checked', 'false');
    }
    }
    item.setAttribute('aria-checked', String(checked));
    emit(menu, 'change', { item, checked });
    }
    function openSub(item, mode) {
    const sub = subOf(item);
    const menu = item.closest('.ds-menu');
    if (!sub || !menu || !enabled(item)) return;
    for (const other of openSubsOf(menu)) if (other !== sub) closeFrom(other, 'parent');
    if (isOpen(sub) && overlays.get(sub)?.opener === item) {
    if (mode === 'first') firstItem(sub)?.focus({ preventScroll: true });
    return;
    }
    const reverse = overlays.get(sub)?.phase === 'closing' && overlays.get(sub)?.opener === item && custom.get(sub)?.anchor === item;
    custom.set(sub, { kind: 'sub', anchor: item });
    focusMode.set(sub, mode);
    openOverlay(sub, { opener: item });
    if (!reverse) enterCustom(sub);
    if (menu.getAnimations?.().some((animation) => animation.playState === 'running')) {
    settled(menu, { transitions: true }).then(() => {
    if (custom.get(sub)?.anchor === item && isOpen(sub)) placeCustom(sub);
    });
    }
    }
    function filter(menu, query) {
    const text = query.replace(/\s+/g, ' ').trim().toLowerCase();
    let shown = 0;
    for (const item of menu.querySelectorAll('.ds-menu__item')) {
    if (item.closest('.ds-menu') !== menu) continue;
    const words = (item.querySelector('.ds-menu__content') ?? item.querySelector('.ds-menu__label') ?? item).textContent.replace(/\s+/g, ' ').toLowerCase();
    const match = !text || words.includes(text);
    setHidden(item, !match);
    if (match) shown += 1;
    }
    for (const group of menu.querySelectorAll('[role="group"]')) {
    if (group.closest('.ds-menu') !== menu) continue;
    const any = [...group.querySelectorAll('.ds-menu__item')].some((item) => !item.hidden);
    setHidden(group, !any);
    }
    for (const divider of menu.querySelectorAll('.ds-menu__divider')) {
    if (divider.closest('.ds-menu') === menu) setHidden(divider, Boolean(text));
    }
    let empty = menu.querySelector('.ds-menu__empty');
    if (!shown && !empty) {
    empty = menu.ownerDocument.createElement('p');
    empty.className = 'ds-menu__empty';
    empty.setAttribute('role', 'none');
    empty.setAttribute('data-ds-keep', '');
    menu.append(empty);
    }
    if (empty) {
    if (!shown) empty.textContent = t('noResults');
    setHidden(empty, shown > 0);
    }
    if (!shown && text) {
    try {
    announce(t('noResults'));
    } catch {
    }
    }
    }
    function setHidden(node, value) {
    if (node.hidden === value) return;
    node.hidden = value;
    if (value) own(node, 'attrs', 'hidden');
    else disown(node, 'attrs', 'hidden');
    }
    function sideKeys(menu) {
    const rtl = viewOf(menu).getComputedStyle?.(menu).direction === 'rtl';
    return rtl ? { into: 'ArrowLeft', out: 'ArrowRight' } : { into: 'ArrowRight', out: 'ArrowLeft' };
    }
    function hoverIntent(menu) {
    let openTimer = (0);
    let closeTimer = (0);
    let pending = (null);
    const time = (token) => motionDuration(menu, token);
    const cancelOpen = () => {
    clearTimeout(openTimer);
    openTimer = 0;
    pending = null;
    };
    const cancelClose = () => {
    clearTimeout(closeTimer);
    closeTimer = 0;
    };
    menu.addEventListener('pointerover', ( event) => {
    if (event.pointerType === 'touch' || !isOpen(menu)) return;
    const target = (event.target);
    if (openSubsOf(menu).some((sub) => sub.contains(target))) {
    cancelClose();
    cancelOpen();
    return;
    }
    if (target.closest?.('.ds-menu') !== menu) return;
    const item = target.closest('.ds-menu__item');
    if (!item || item === pending) return;
    cancelOpen();
    const sub = subOf(item);
    const others = openSubsOf(menu).filter((other) => other !== sub);
    if (others.length && !closeTimer) {
    closeTimer = setTimeout(() => {
    closeTimer = 0;
    for (const other of openSubsOf(menu)) if (overlays.get(other)?.opener !== pending && overlays.get(other)?.opener !== item) closeFrom(other, 'parent');
    }, time('--ds-delay-submenu-close'));
    }
    if (sub && enabled(item) && !(isOpen(sub) && overlays.get(sub)?.opener === item)) {
    pending = item;
    openTimer = setTimeout(() => {
    openTimer = 0;
    pending = null;
    if (!isOpen(menu)) return;
    cancelClose();
    openSub(item, 'none');
    }, time('--ds-delay-submenu'));
    } else if (sub) cancelClose();
    });
    menu.addEventListener('pointerleave', () => cancelOpen());
    menu.addEventListener('click', () => cancelOpen(), true);
    menu.addEventListener('keydown', () => cancelOpen(), true);
    }
    function wireMenu(menu) {
    let typed = '';
    let typedAt = 0;
    menu.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    const target = (event.target);
    if (target?.closest?.('.ds-menu') !== menu) return;
    const items = menuItems(menu);
    const search = searchOf(menu);
    const keys = sideKeys(menu);
    const item = target.closest?.('.ds-menu__item');
    if (search && target === search) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    (event.key === 'ArrowDown' ? items[0] : items.at(-1))?.focus();
    } else if (event.key === 'Enter') {
    const first = items.find(enabled);
    if (!first) return;
    event.preventDefault();
    (first).click();
    }
    return;
    }
    if (item && event.key === keys.into && subOf(item)) {
    event.preventDefault();
    openSub(item, 'first');
    return;
    }
    if (event.key === keys.out && custom.get(menu)?.kind === 'sub') {
    event.preventDefault();
    closeOverlay(menu, { reason: 'collapse' });
    return;
    }
    if (search && ((event.key === 'ArrowUp' && item === items[0]) || (event.key === 'ArrowDown' && item === items.at(-1)))) {
    event.preventDefault();
    search.focus();
    return;
    }
    const next = arrowTarget(items, event);
    if (next) {
    typed = '';
    event.preventDefault();
    next.focus();
    return;
    }
    if (event.key.length !== 1 || (event.key === ' ' && !typed)) return;
    if (search) {
    event.preventDefault();
    search.focus();
    search.value += event.key;
    search.dispatchEvent(new (viewOf(menu).Event)('input', { bubbles: true }));
    return;
    }
    const now = clock();
    if (now - typedAt > motionDuration(menu, '--ds-delay-tooltip')) typed = '';
    typedAt = now;
    typed += event.key.toLowerCase();
    const match = typeAhead(items, item, typed);
    if (!match) return;
    event.preventDefault();
    match.focus();
    });
    menu.addEventListener('input', (event) => {
    const search = searchOf(menu);
    if (search && event.target === search) filter(menu, search.value);
    });
    menu.addEventListener('click', ( event) => {
    const item = (event.target)?.closest?.('.ds-menu__item');
    if (!item || item.closest('.ds-menu') !== menu || !enabled(item) || (item).disabled) return;
    const state = overlays.get(menu);
    if (state?.phase !== 'open') return;
    if (subOf(item)) {
    openSub(item, event.detail === 0 ? 'first' : 'none');
    return;
    }
    const gen = state.gen;
    flipCheck(menu, item);
    setTimeout(() => {
    if (state.gen === gen && state.phase === 'open') closeChain(menu, 'item');
    });
    });
    hoverIntent(menu);
    }
    function focusInto(menu) {
    const mode = focusMode.get(menu);
    focusMode.delete(menu);
    const search = searchOf(menu);
    if (search) {
    if (search.value) {
    search.value = '';
    filter(menu, '');
    }
    if (mode !== 'none') search.focus({ preventScroll: true });
    return;
    }
    if (mode === 'none') return;
    const event = (viewOf(menu)).event;
    const last = mode === 'last' || (event?.type === 'keydown' && event.key === 'ArrowUp');
    (last ? menuItems(menu).filter(enabled).at(-1) : firstItem(menu))?.focus({ preventScroll: true });
    }
    function open(element, options = {}) {
    custom.delete(element);
    return openOverlay(element, options);
    }
    function openAt(element, options = {}) {
    if (!element?.isConnected) return Promise.resolve();
    const opener = options.opener ?? activeIn(element) ?? null;
    const point = Number.isFinite(options.x) && Number.isFinite(options.y) ? { x: Number(options.x), y: Number(options.y) } : null;
    const expanded = opener?.getAttribute?.('aria-expanded') ?? null;
    const before = custom.get(element);
    const reverse = Boolean(point) && overlays.get(element)?.phase === 'closing' && overlays.get(element)?.opener === opener
    && before?.kind === 'point' && before.anchor.x === point.x && before.anchor.y === point.y;
    if (point) custom.set(element, { kind: 'point', anchor: point });
    else custom.delete(element);
    let entrance = openOverlay(element, { opener, returnTo: options.returnTo });
    if (point && !reverse) {
    enterCustom(element);
    entrance = settled(element, { transitions: true });
    }
    if (opener && (expanded === null || unexpanded.get(element) === opener)) {
    opener.removeAttribute('aria-expanded');
    unexpanded.set(element, opener);
    } else unexpanded.delete(element);
    return entrance;
    }
    function close(element, options = {}) {
    return closeOverlay(element, options);
    }
    function create(element) {
    return controllerOf(element);
    }
    define('menu', {
    attribute: 'data-ds-menu',
    init: (element) => create(element),
    });
    function contextMenuOf(target) {
    const value = target.getAttribute('data-ds-contextmenu') ?? '';
    if (!value) return null;
    const scope = target.getRootNode?.() ?? target.ownerDocument;
    let found = null;
    try {
    found = scope.querySelector(value);
    } catch {
    }
    found ??= scope.getElementById?.(value) ?? null;
    return found?.matches('.ds-menu') ? found : null;
    }
    const PRESS_SLOP_PX = 10;
    const handled = new WeakSet();
    define('contextmenu', {
    attribute: 'data-ds-contextmenu',
    global(root) {
    const doc = root.nodeType === 9 ? (root) : root.ownerDocument;
    if (!doc) return () => {};
    let press = (null);
    let swallow = (null);
    let keyOpen = { at: -Infinity, within: 0 };
    const targetOf = ( event) => ( (event.composedPath?.()[0] ?? event.target)?.closest?.('[data-ds-contextmenu]') ?? null);
    const show = (target, point) => {
    const menu = contextMenuOf(target);
    if (!menu) return false;
    const rect = target.getBoundingClientRect();
    const at = point ?? { x: rect.left, y: rect.bottom };
    if (!emit(target, 'contextmenu', { menu, x: at.x, y: at.y }, { cancelable: true })) return false;
    const active = activeIn(target);
    const opener = active && target.contains(active) && active !== doc.body ? active : target;
    openAt(menu, { x: at.x, y: at.y, opener });
    return true;
    };
    const unswallow = () => {
    if (!swallow) return;
    doc.removeEventListener('click', swallow, true);
    doc.removeEventListener('contextmenu', swallow, true);
    swallow = null;
    };
    const cancelPress = () => {
    if (press) clearTimeout(press.timer);
    press = null;
    };
    const onContextMenu = ( event) => {
    if (handled.has(event)) return;
    handled.add(event);
    if (event.defaultPrevented) return;
    if (clock() - keyOpen.at < keyOpen.within) {
    keyOpen = { at: -Infinity, within: 0 };
    event.preventDefault();
    return;
    }
    const target = targetOf(event);
    if (!target) return;
    cancelPress();
    const keyboard = event.button !== 2 && event.clientX === 0 && event.clientY === 0;
    if (show(target, keyboard ? null : { x: event.clientX, y: event.clientY })) event.preventDefault();
    };
    const onKeyDown = ( event) => {
    if (handled.has(event)) return;
    handled.add(event);
    const contextKey = event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey);
    if (!contextKey || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    const target = targetOf(event);
    if (!target || !show(target, null)) return;
    keyOpen = { at: clock(), within: motionDuration(target, '--ds-delay-dismiss') };
    event.preventDefault();
    };
    const onPointerDown = ( event) => {
    unswallow();
    cancelPress();
    if (event.pointerType !== 'touch' || handled.has(event)) return;
    handled.add(event);
    const target = targetOf(event);
    if (!target || !contextMenuOf(target)) return;
    const x = event.clientX;
    const y = event.clientY;
    press = { target, id: event.pointerId, x, y, timer: 0 };
    press.timer = setTimeout(() => {
    if (!press || press.target !== target) return;
    press = null;
    if (!show(target, { x, y })) return;
    swallow = ( next) => {
    next.preventDefault();
    next.stopImmediatePropagation();
    if (next.type === 'click') unswallow();
    };
    doc.addEventListener('click', swallow, true);
    doc.addEventListener('contextmenu', swallow, true);
    }, motionDuration(target, '--ds-delay-long-press'));
    };
    const onPointerMove = ( event) => {
    if (!press || event.pointerId !== press.id) return;
    if (Math.hypot(event.clientX - press.x, event.clientY - press.y) > PRESS_SLOP_PX) cancelPress();
    };
    const onPointerEnd = ( event) => {
    if (press && event.pointerId === press.id) cancelPress();
    };
    root.addEventListener('contextmenu', onContextMenu);
    root.addEventListener('keydown', onKeyDown);
    root.addEventListener('pointerdown', onPointerDown);
    root.addEventListener('pointermove', onPointerMove, { passive: true });
    root.addEventListener('pointerup', onPointerEnd);
    root.addEventListener('pointercancel', onPointerEnd);
    return () => {
    cancelPress();
    unswallow();
    root.removeEventListener('contextmenu', onContextMenu);
    root.removeEventListener('keydown', onKeyDown);
    root.removeEventListener('pointerdown', onPointerDown);
    root.removeEventListener('pointermove', onPointerMove);
    root.removeEventListener('pointerup', onPointerEnd);
    root.removeEventListener('pointercancel', onPointerEnd);
    };
    },
    });
    internals.use('menu', {
    focusInto,
    wire(menu) {
    listen(menu.ownerDocument);
    wireMenu(menu);
    },
    });

    return Object.freeze({
      open: open,
      openAt: openAt,
      close: close,
      create: create
    });
  });

  add('page', ['fold', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const own = DS.dom.own, disown = DS.dom.disown;
    const iconElement = DS.icons.iconElement, moreVertical = DS.icons.moreVertical;
    const openMenu = DS.menu.open, closeMenu = DS.menu.close;
    let serial = 0;
    const TOP = 1;
    function fold(widths, options) {
    const { room, gap = 0, more = 0, order = widths.map((_, index) => index) } = options;
    const need = (kept, withMore) => {
    const parts = kept.map((index) => widths[index]);
    if (withMore) parts.push(more);
    return parts.length ? parts.reduce((sum, width) => sum + width, 0) + gap * (parts.length - 1) : 0;
    };
    const all = widths.map((_, index) => index);
    if (need(all, false) <= room) return [];
    const folded = new Set();
    for (const index of order) {
    folded.add(index);
    if (need(all.filter((each) => !folded.has(each)), true) <= room) break;
    }
    return [...folded].sort((a, b) => a - b);
    }
    function scrolls(element, view) {
    const style = view.getComputedStyle(element);
    return /(auto|scroll|overlay)/.test(style.overflowY) && element.scrollHeight > element.clientHeight;
    }
    function scrollerOf(element, view) {
    const doc = element.ownerDocument;
    for (let node = element.parentElement; node && node !== doc.body && node !== doc.documentElement; node = node.parentElement) {
    if (scrolls(node, view)) return node;
    }
    return null;
    }
    function labelOf(action) {
    const copy = action.cloneNode(true);
    for (const part of copy.querySelectorAll('svg, .ds-sr, .ds-badge')) part.remove();
    return copy.textContent.trim() || action.getAttribute('aria-label') || action.getAttribute('data-tip') || action.getAttribute('title') || '';
    }
    function controller(head, options, signal) {
    const doc = head.ownerDocument;
    const view = doc.defaultView ?? globalThis;
    const abort = new AbortController();
    const listen = { signal: abort.signal };
    signal?.addEventListener('abort', () => abort.abort(), { once: true });
    const listeners = new Set();
    const owned = [];
    const take = (element, name) => {
    if (owned.some(([each, which]) => each === element && which === name)) return;
    own(element, 'attrs', name);
    owned.push([element, name]);
    };
    const sticky = options.condense !== false && head.classList.contains('ds-page-head--sticky');
    const scroller = sticky ? scrollerOf(head, view) : null;
    const box = () => scroller ?? doc.scrollingElement ?? doc.documentElement;
    let condensed = head.hasAttribute('data-scrolled');
    let foldHeight = 0;
    let offset = 0;
    if (sticky) {
    take(head, 'data-scrolled');
    take(head, 'style');
    }
    const quiet = (fn) => {
    const target = scroller ?? doc.documentElement;
    const before = target.style.overflowAnchor;
    target.style.overflowAnchor = 'none';
    try {
    fn();
    } finally {
    target.style.overflowAnchor = before;
    }
    };
    const apply = (on, fold = 0) => {
    if (on) {
    head.setAttribute('data-scrolled', '');
    head.style.setProperty('--ds-page-head-fold', `${fold}px`);
    } else {
    head.removeAttribute('data-scrolled');
    head.style.removeProperty('--ds-page-head-fold');
    }
    };
    const measureFold = () => {
    if (!sticky || condensed) return;
    const start = box().scrollTop;
    const settling = !head.hasAttribute('data-ds-moving');
    if (settling) head.setAttribute('data-ds-moving', '');
    const scrolling = box();
    const anchor = scrolling.style.overflowAnchor;
    scrolling.style.overflowAnchor = 'none';
    quiet(() => {
    const before = head.style.position;
    head.style.position = 'static';
    const place = head.getBoundingClientRect().top;
    head.style.position = before;
    const stick = parseFloat(view.getComputedStyle(head).top) || 0;
    const origin = scroller ? scroller.getBoundingClientRect().top + scroller.clientTop : 0;
    offset = Math.max(0, Math.round(place - origin + start - stick));
    const tall = head.getBoundingClientRect().height;
    apply(true);
    foldHeight = Math.max(0, tall - head.getBoundingClientRect().height);
    apply(false);
    });
    if (settling) {
    void view.getComputedStyle(head).opacity;
    head.removeAttribute('data-ds-moving');
    }
    scrolling.style.overflowAnchor = anchor;
    if (Math.abs(box().scrollTop - start) >= 1) box().scrollTop = start;
    };
    const condense = (on, force = false) => {
    if (on === condensed) return false;
    if (on) {
    if (!force && box().scrollTop <= offset + foldHeight + TOP) return false;
    quiet(() => {
    const tall = head.getBoundingClientRect().height;
    apply(true, foldHeight);
    const fold = Math.max(0, tall - head.getBoundingClientRect().height);
    if (Math.abs(fold - foldHeight) > 0.01) {
    foldHeight = fold;
    apply(true, fold);
    }
    });
    } else quiet(() => apply(false));
    condensed = on;
    layout();
    const detail = { condensed: on };
    emit(head, 'change', detail);
    for (const fn of listeners) fn(detail);
    return true;
    };
    let frame = 0;
    const update = () => {
    frame = 0;
    if (!sticky) return;
    const top = box().scrollTop;
    if (!condensed && top > offset + foldHeight + TOP) condense(true);
    else if (condensed && top <= offset + foldHeight) condense(false);
    };
    const later = () => {
    if (frame) return;
    frame = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(update) : 0;
    if (!frame) update();
    };
    if (sticky) (scroller ?? view).addEventListener('scroll', later, { passive: true, signal: abort.signal });
    let folds = 0;
    const heads = sticky && typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(() => {
    if (condensed || folds) return;
    folds = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(() => { folds = 0; measureFold(); update(); }) : 0;
    }) : null;
    heads?.observe(head);
    measureFold();
    const actions = head.querySelector('.ds-page-head__actions');
    const row = actions?.closest('.ds-page-head__row') ?? actions?.parentElement ?? null;
    const folded = new Set();
    const itemsOf = new WeakMap();
    let more = null;
    let menu = null;
    const actionList = () => (actions ? [...actions.children].filter((child) => child !== more && child !== menu && !child.classList.contains('ds-menu')) : []);
    if (actions) {
    serial += 1;
    const menuId = `ds-page-more-${serial}`;
    more = doc.createElement('button');
    more.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-page-head__more';
    more.type = 'button';
    more.hidden = true;
    more.setAttribute('aria-haspopup', 'menu');
    more.setAttribute('aria-expanded', 'false');
    more.setAttribute('aria-controls', menuId);
    more.setAttribute('aria-label', t('moreActions'));
    more.setAttribute('data-tip', t('moreActions'));
    more.dataset.dsKeep = '';
    more.append(iconElement(moreVertical, 20, '', doc));
    menu = doc.createElement('div');
    menu.className = 'ds-menu';
    menu.id = menuId;
    menu.hidden = true;
    menu.setAttribute('role', 'menu');
    menu.setAttribute('aria-label', t('moreActions'));
    menu.dataset.dsKeep = '';
    actions.append(more, menu);
    more.addEventListener('click', (event) => {
    if (event.detail > 1) return;
    if (more.getAttribute('aria-expanded') === 'true') closeMenu(menu);
    else openMenu(menu, { opener: more });
    }, listen);
    more.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    openMenu(menu, { opener: more });
    }, listen);
    menu.addEventListener('click', (event) => {
    const item = (event.target)?.closest?.('.ds-menu__item');
    const action = item ? itemsOf.get(item) : null;
    if (!action || item.getAttribute('aria-disabled') === 'true') return;
    if (item.tagName === 'A') {
    closeMenu(menu);
    return;
    }
    closeMenu(menu);
    action.click();
    }, listen);
    }
    const fillMenu = (list) => {
    if (!menu) return;
    const items = list.map((action) => {
    const link = action.tagName === 'A' && action.hasAttribute('href');
    const item = doc.createElement(link ? 'a' : 'button');
    item.className = 'ds-menu__item';
    item.setAttribute('role', 'menuitem');
    if (link) {
    item.setAttribute('href', action.getAttribute('href'));
    for (const name of ['target', 'rel', 'download']) if (action.hasAttribute(name)) item.setAttribute(name, action.getAttribute(name));
    } else item.type = 'button';
    if (action.matches(':disabled') || action.getAttribute('aria-disabled') === 'true' || action.getAttribute('aria-busy') === 'true') item.setAttribute('aria-disabled', 'true');
    const glyph = action.querySelector(':scope > svg');
    if (glyph) item.append(glyph.cloneNode(true));
    const label = doc.createElement('span');
    label.className = 'ds-menu__label';
    label.textContent = labelOf(action);
    item.append(label);
    itemsOf.set(item, action);
    return item;
    });
    menu.replaceChildren(...items);
    };
    function layout() {
    if (!more || !actions.isConnected || !row) return;
    for (const action of folded) action.hidden = false;
    folded.clear();
    more.hidden = true;
    const all = actionList().filter((action) => !action.hidden);
    const style = view.getComputedStyle(actions);
    const gap = parseFloat(style.columnGap) || 0;
    const widths = all.map((action) => action.getBoundingClientRect().width);
    more.hidden = false;
    const moreWidth = more.getBoundingClientRect().width;
    more.hidden = true;
    const order = all.map((_, index) => index).sort((a, b) => Number(all[a].classList.contains('ds-btn--filled')) - Number(all[b].classList.contains('ds-btn--filled')) || a - b);
    const out = fold(widths, { room: condensed ? row.clientWidth / 2 : row.clientWidth, gap, more: moreWidth, order });
    if (!out.length) {
    fillMenu([]);
    if (more.getAttribute('aria-expanded') === 'true') closeMenu(menu);
    return;
    }
    const list = [];
    for (const index of out) {
    const action = all[index];
    take(action, 'hidden');
    action.hidden = true;
    folded.add(action);
    list.push(action);
    }
    more.hidden = false;
    fillMenu(list);
    }
    let layoutFrame = 0;
    const relayout = () => {
    if (layoutFrame) return;
    layoutFrame = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(() => { layoutFrame = 0; layout(); }) : 0;
    if (!layoutFrame) layout();
    };
    const resize = more && typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(() => layout()) : null;
    resize?.observe(row);
    const helper = (node) => node === menu || node === more || (node.nodeType === 1 && (node).hasAttribute('data-ds-keep') && (node).getAttribute('aria-hidden') === 'true');
    const helpersOnly = (record) => record.type === 'childList' && [...record.addedNodes, ...record.removedNodes].every(helper);
    const changes = more && typeof view.MutationObserver === 'function' ? new view.MutationObserver((records) => {
    if (records.every((record) => record.target === menu || menu.contains(record.target) || record.target === more || helpersOnly(record))) return;
    relayout();
    }) : null;
    changes?.observe(actions, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['disabled', 'aria-disabled', 'aria-label'] });
    layout();
    doc.fonts?.ready?.then(() => { if (!abort.signal.aborted) layout(); });
    update();
    let gone = false;
    return {
    get() {
    return condensed;
    },
    set(value) {
    if (sticky) condense(Boolean(value), true);
    },
    on(type, fn) {
    if (type !== 'change') return () => {};
    listeners.add(fn);
    return () => listeners.delete(fn);
    },
    refresh() {
    layout();
    measureFold();
    update();
    },
    destroy() {
    if (gone) return;
    gone = true;
    abort.abort();
    listeners.clear();
    resize?.disconnect();
    changes?.disconnect();
    heads?.disconnect();
    if (folds) view.cancelAnimationFrame?.(folds);
    if (frame) view.cancelAnimationFrame?.(frame);
    if (layoutFrame) view.cancelAnimationFrame?.(layoutFrame);
    for (const action of folded) action.hidden = false;
    folded.clear();
    if (menu && more?.getAttribute('aria-expanded') === 'true') closeMenu(menu);
    more?.remove();
    menu?.remove();
    for (const [element, name] of owned) disown(element, 'attrs', name);
    owned.length = 0;
    },
    };
    }
    define('page', {
    attribute: 'data-ds-page-head',
    attributes: ['data-ds-page-head-condense'],
    init(el, ctx) {
    return controller( (el), { condense: el.getAttribute('data-ds-page-head-condense') !== 'off' }, ctx.signal);
    },
    });
    function create(el, options = {}) {
    return controller(el, { condense: options.condense !== false });
    }

    return Object.freeze({
      fold: fold,
      create: create
    });
  });

  add('sheet', ['MIN_WIDTH', 'open', 'close', 'clampWidth', 'dragWidth', 'resizable', 'create'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, t = DS.core.t;
    const own = DS.dom.own, watchScrolled = DS.dom.watchScrolled;
    const internals = DS.overlay.internals, openOverlay = DS.overlay.open, closeOverlay = DS.overlay.close;
    const tabbables = DS.a11y.tabbables;
    const settled = DS.motion.settled;
    const openStore = DS.store.open;
    const { controllerOf, activeIn } = internals;
    const DOCKED = '.ds-sheet__panel--push, .ds-sheet__panel--inspector';
    const MIN_WIDTH = 280;
    function open(element, options = {}) {
    return openOverlay(element, options);
    }
    function close(element, options = {}) {
    return closeOverlay(element, options);
    }
    function panelOf(element) {
    if (typeof element?.matches !== 'function') return null;
    if (element.matches('.ds-sheet__panel')) return element;
    return element.querySelector('.ds-sheet__panel') ?? element.closest('.ds-sheet__panel');
    }
    function numberOf(element, name, fallback) {
    const value = Number.parseFloat(element.getAttribute(name) ?? '');
    return Number.isFinite(value) ? value : fallback;
    }
    function boundsOf(panel, owner = panel) {
    const read = (name, fallback) => numberOf(panel, name, numberOf(owner, name, fallback));
    const min = Math.max(0, read('data-ds-sheet-min', MIN_WIDTH));
    const docked = panel.matches(DOCKED);
    const around = docked ? panel.closest('.ds-sheet__layout') : null;
    const room = around ? around.getBoundingClientRect().width : (panel.ownerDocument.defaultView?.innerWidth ?? 1280);
    const max = Math.max(min, read('data-ds-sheet-max', room - MIN_WIDTH));
    return { min, max };
    }
    function clampWidth(width, bounds) {
    return Math.round(Math.min(bounds.max, Math.max(bounds.min, width)));
    }
    function dragWidth(start, delta, side, bounds) {
    return clampWidth(start + (side === 'left' ? -delta : delta), bounds);
    }
    const resizers = new WeakMap();
    function resizable(element, options = {}) {
    const panel = panelOf(element);
    if (!panel) return null;
    const known = resizers.get(panel);
    if (known) return known;
    const handle = panel.querySelector(':scope > .ds-sheet__resize');
    const owner = element;
    const abort = new AbortController();
    const signal = abort.signal;
    const key = options.remember ?? owner.getAttribute('data-ds-sheet-remember') ?? panel.getAttribute('data-ds-sheet-remember') ?? '';
    const store = key ? openStore('sheet') : null;
    const listeners = [];
    const bounds = () => {
    const found = boundsOf(panel, owner);
    return {
    min: options.min ?? found.min,
    max: Math.max(options.min ?? found.min, options.max ?? found.max),
    };
    };
    own(panel, 'props', '--ds-sheet-size');
    own(panel, 'attrs', 'data-resizing');
    let width = null;
    const current = () => width ?? Math.round(panel.getBoundingClientRect().width);
    const paint = () => {
    if (width == null) (panel).style.removeProperty('--ds-sheet-size');
    else (panel).style.setProperty('--ds-sheet-size', `${width}px`);
    if (!handle) return;
    const { min, max } = bounds();
    handle.setAttribute('aria-valuemin', String(Math.round(min)));
    handle.setAttribute('aria-valuemax', String(Math.round(max)));
    const now = current();
    if (now > 0) handle.setAttribute('aria-valuenow', String(Math.min(max, Math.max(min, now))));
    };
    const commit = () => {
    if (store) store.set(key, width ?? undefined);
    emit(panel, 'resize', { width: current() });
    };
    const apply = ( next, save = true) => {
    width = next == null ? null : clampWidth(next, bounds());
    paint();
    if (save) commit();
    };
    if (handle) {
    if (!handle.hasAttribute('role')) handle.setAttribute('role', 'separator');
    handle.setAttribute('aria-orientation', 'vertical');
    if (!handle.hasAttribute('tabindex')) handle.setAttribute('tabindex', '0');
    if (!handle.hasAttribute('aria-label') && !handle.hasAttribute('aria-labelledby')) handle.setAttribute('aria-label', t('resizePanel'));
    if (panel.id) handle.setAttribute('aria-controls', panel.id);
    const sideOf = () => {
    const box = handle.getBoundingClientRect();
    const around = panel.getBoundingClientRect();
    return box.left + box.width / 2 < around.left + around.width / 2 ? 'left' : 'right';
    };
    handle.addEventListener('pointerdown', ( event) => {
    if (event.button !== 0 || !event.isPrimary) return;
    event.preventDefault();
    const start = current();
    const startX = event.clientX;
    const side = sideOf();
    const limits = bounds();
    handle.setPointerCapture?.(event.pointerId);
    panel.setAttribute('data-resizing', '');
    const move = ( moved) => {
    if (moved.pointerId !== event.pointerId) return;
    width = dragWidth(start, moved.clientX - startX, side, limits);
    paint();
    };
    const end = ( ended) => {
    if (ended.pointerId !== event.pointerId) return;
    handle.removeEventListener('pointermove', move);
    handle.removeEventListener('pointerup', end);
    handle.removeEventListener('pointercancel', end);
    panel.removeAttribute('data-resizing');
    if (width != null && width !== start) commit();
    };
    handle.addEventListener('pointermove', move, { signal });
    handle.addEventListener('pointerup', end, { signal });
    handle.addEventListener('pointercancel', end, { signal });
    }, { signal });
    handle.addEventListener('dblclick', () => apply(null), { signal });
    handle.addEventListener('keydown', ( event) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const limits = bounds();
    const view = panel.ownerDocument.defaultView;
    const unit = Number.parseFloat(view?.getComputedStyle(handle).getPropertyValue('--ds-space-4') ?? '') || 16;
    const step = event.shiftKey ? unit * 4 : unit;
    const side = sideOf();
    let next = null;
    if (event.key === 'ArrowLeft') next = current() + (side === 'left' ? step : -step);
    else if (event.key === 'ArrowRight') next = current() + (side === 'left' ? -step : step);
    else if (event.key === 'Home') next = limits.min;
    else if (event.key === 'End') next = limits.max;
    else return;
    event.preventDefault();
    apply(next);
    }, { signal });
    }
    const stored = store?.get(key, null);
    if (typeof stored === 'number' && Number.isFinite(stored)) {
    width = Math.round(stored);
    paint();
    } else paint();
    const view = panel.ownerDocument.defaultView;
    const room = panel.matches(DOCKED) ? panel.closest('.ds-sheet__layout') : panel.ownerDocument.documentElement;
    const sizes = room && typeof view?.ResizeObserver === 'function' ? new view.ResizeObserver(() => {
    if (width != null) width = clampWidth(width, bounds());
    paint();
    }) : null;
    sizes?.observe(room);
    owner.addEventListener('ds:open', (event) => {
    if (event.target === panel || event.target === owner) settled(panel, { transitions: true }).then(paint);
    }, { signal });
    const controller = {
    element: panel,
    get: () => width,
    set: ( value) => apply(value == null ? null : Number(value)),
    on( type, fn) {
    const name = `ds:${type}`;
    panel.addEventListener(name, fn);
    const off = () => panel.removeEventListener(name, fn);
    listeners.push(off);
    return off;
    },
    destroy() {
    if (resizers.get(panel) !== controller) return;
    resizers.delete(panel);
    abort.abort();
    sizes?.disconnect();
    for (const off of listeners.splice(0)) off();
    store?.destroy();
    },
    };
    resizers.set(panel, controller);
    return controller;
    }
    function create(element) {
    const base = controllerOf(element);
    const panel = panelOf(element);
    const sizer = panel?.querySelector(':scope > .ds-sheet__resize') ? resizable(element) : null;
    return {
    ...base,
    destroy() {
    base.destroy();
    sizer?.destroy();
    },
    };
    }
    function dockedOpened(panel) {
    for (const body of panel.querySelectorAll(':scope > .ds-sheet__body')) {
    const handle = watchScrolled(body);
    if (!body.querySelector(':scope > [data-ds-sentinel]')) handle.update();
    }
    if (!panel.matches('.ds-sheet__panel--push')) return;
    const active = activeIn(panel);
    if (active && panel.contains(active)) return;
    const target = tabbables(panel).find((node) => !node.matches('.ds-sheet__resize'));
    if (target) target.focus({ preventScroll: true });
    else {
    if (!panel.hasAttribute('tabindex')) panel.setAttribute('tabindex', '-1');
    panel.focus({ preventScroll: true });
    }
    }
    define('sheet', {
    attribute: 'data-ds-sheet',
    init: (element) => create(element),
    global(root) {
    const onOpen = ( event) => {
    const panel = (event.target);
    if (panel?.matches?.(DOCKED)) dockedOpened(panel);
    };
    root.addEventListener('ds:open', onOpen);
    return () => root.removeEventListener('ds:open', onOpen);
    },
    });

    return Object.freeze({
      MIN_WIDTH: MIN_WIDTH,
      open: open,
      close: close,
      clampWidth: clampWidth,
      dragWidth: dragWidth,
      resizable: resizable,
      create: create
    });
  });

  add('shell', ['SEARCH_QUERY', 'matchText', 'create', 'drawer', 'searchSheet', 'launcher', 'setScheme', 'schemeSwitch', 'fitCrumbs', 'breadcrumbs'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define, emit = DS.core.emit, get = DS.core.get, runtime = DS.core.runtime, t = DS.core.t;
    const motionDuration = DS.motion.motionDuration;
    const disown = DS.dom.disown, own = DS.dom.own, scrollEdges = DS.dom.scrollEdges, watchScrolled = DS.dom.watchScrolled;
    const iconElement = DS.icons.iconElement, moreHorizontal = DS.icons.moreHorizontal;
    const DRAWER_QUERY = DS.overlay.DRAWER_QUERY, closeOverlay = DS.overlay.close, lockScroll = DS.overlay.lockScroll, openOverlay = DS.overlay.open, unlockScroll = DS.overlay.unlockScroll;
    const indicator = DS.indicator.create;
    const announce = DS.a11y.announce, canTakeFocus = DS.a11y.canTakeFocus, inert = DS.a11y.inert, tabbables = DS.a11y.tabbables;
    const plural = DS.format.plural;
    const openStore = DS.store.open;
    const openMenu = DS.menu.open, closeMenu = DS.menu.close;
    const SEARCH_QUERY = '(max-width: 720px)';
    const NAMESPACE = 'shell';
    const SCHEMES = new Set(['light', 'dark', 'auto']);
    const KEEP = ['data', 'ds', 'keep'].join('-');
    function marker(name) {
    const claims = new Set();
    const set = (node, on) => {
    node.toggleAttribute(name, on);
    if (on && !claims.has(node)) {
    own(node, 'attrs', name);
    claims.add(node);
    } else if (!on && claims.has(node)) {
    disown(node, 'attrs', name);
    claims.delete(node);
    }
    };
    set.release = (root) => {
    for (const node of [...claims]) if (!root || !root.contains(node)) set(node, false);
    };
    return set;
    }
    const shared = runtime.state('shell', () => ({ seq: 0 }));
    const viewOf = (node) => node?.ownerDocument?.defaultView ?? globalThis;
    const media = (node, query) => {
    const view = viewOf(node);
    return typeof view.matchMedia === 'function' ? view.matchMedia(query) : null;
    };
    function idOf(element, prefix) {
    if (!element.id) element.id = `ds-${prefix}-${shared.seq += 1}`;
    return element.id;
    }
    const prefsOf = (remember) => (remember === false ? null : openStore(NAMESPACE));
    const keyOf = (value, fallback) => (typeof value === 'string' && value.trim() ? value.trim() : fallback);
    function listeners(element, signal) {
    const offs = new Set();
    return {
    on(type, fn) {
    const handler = ( event) => { if (event.target === element) fn(event.detail); };
    element.addEventListener(`ds:${type}`, handler, signal ? { signal } : undefined);
    const off = () => { element.removeEventListener(`ds:${type}`, handler); offs.delete(off); };
    offs.add(off);
    return off;
    },
    clear() { for (const off of [...offs]) off(); },
    };
    }
    function normalise(text) {
    return String(text ?? '').normalize('NFD').replace(/\p{M}+/gu, '').toLowerCase().replace(/\s+/g, ' ').trim();
    }
    function matchText(text, query) {
    const words = normalise(query).split(' ').filter(Boolean);
    if (!words.length) return true;
    const haystack = normalise(text);
    return words.every((word) => haystack.includes(word));
    }
    function create(app, options = {}) {
    return frame(app, options, null);
    }
    function frame(app, options, signal) {
    const doc = app.ownerDocument;
    const drawer = app.querySelector(':scope > .ds-drawer');
    const menu = app.querySelector(':scope > .ds-topbar .ds-topbar__menu');
    const view = app.querySelector(':scope > .ds-panel > .ds-view') ?? app.querySelector('.ds-view');
    const list = drawer?.querySelector('.ds-drawer__list') ?? null;
    const railable = options.rail !== false;
    const key = `rail:${keyOf(options.key, 'app')}`;
    const prefs = prefsOf(options.remember);
    const modal = media(app, DRAWER_QUERY);
    const events = listeners(app, signal);
    const abort = new AbortController();
    const on = { signal: abort.signal };
    if (drawer && menu) menu.setAttribute('aria-controls', idOf(drawer, 'drawer'));
    const rail = () => app.hasAttribute('data-drawer-collapsed');
    const isOpen = () => app.hasAttribute('data-drawer-open');
    const sync = () => {
    if (!menu) return;
    if (modal?.matches) {
    menu.removeAttribute('aria-pressed');
    menu.setAttribute('aria-expanded', String(isOpen()));
    } else {
    menu.removeAttribute('aria-expanded');
    if (railable && drawer) menu.setAttribute('aria-pressed', String(rail()));
    else menu.removeAttribute('aria-pressed');
    }
    };
    const setRail = (value, remember) => {
    if (!railable || value === rail()) return;
    app.toggleAttribute('data-drawer-collapsed', value);
    if (value && drawer) get(drawer, 'drawer')?.set({ query: '' });
    if (remember) prefs?.set(key, value);
    sync();
    emit(app, 'change', { rail: value });
    };
    const openDrawer = () => (drawer && modal?.matches ? openOverlay(drawer, { opener: menu ?? undefined }) : Promise.resolve());
    const closeDrawer = () => (drawer && isOpen() ? closeOverlay(drawer) : Promise.resolve());
    const stored = prefs?.get(key);
    if (railable && typeof stored === 'boolean') app.toggleAttribute('data-drawer-collapsed', stored);
    if (!railable) app.removeAttribute('data-drawer-collapsed');
    sync();
    menu?.addEventListener('click', ( event) => {
    if (event.defaultPrevented || !drawer) return;
    if (modal?.matches) {
    if (isOpen()) closeDrawer();
    else openDrawer();
    } else setRail(!rail(), true);
    }, on);
    app.addEventListener('click', ( event) => {
    const target = (event.target);
    const skip = target?.closest?.('.ds-skip');
    if (skip && app.contains(skip)) {
    skipTo(skip, event);
    return;
    }
    const item = target?.closest?.('.ds-drawer__item');
    if (item && drawer?.contains(item) && isOpen() && item.getAttribute('aria-disabled') !== 'true') closeDrawer();
    }, on);
    const skipTo = (skip, event) => {
    const href = skip.getAttribute('href') ?? '';
    if (!href.startsWith('#') || href.length < 2) return;
    const target = doc.getElementById(decodeURIComponent(href.slice(1)));
    if (!target) return;
    event.preventDefault();
    if (drawer && (drawer === target || drawer.contains(target)) && modal?.matches) {
    openDrawer();
    return;
    }
    const sheet = target.closest('[data-ds-search-sheet]');
    if (sheet && media(sheet, SEARCH_QUERY)?.matches) {
    get(sheet, 'search-sheet')?.set(true);
    return;
    }
    const focusable = canTakeFocus(target) && (target.matches('a[href], button, input, select, textarea, summary') || target.hasAttribute('tabindex'));
    if (!focusable) target.setAttribute('tabindex', '-1');
    (target).focus();
    };
    const onMedia = () => sync();
    modal?.addEventListener('change', onMedia);
    drawer?.addEventListener('ds:close', (event) => { if (event.target === drawer) sync(); }, on);
    const handles = [];
    if (list) {
    handles.push(indicator( (list), { active: '[aria-current="page"]', className: 'ds-drawer__indicator', reveal: true }));
    handles.push(scrollEdges(list, { axis: 'block' }));
    }
    if (view) handles.push(watchScrolled(view, app));
    let alive = true;
    return {
    element: app,
    get: () => ({ rail: rail(), open: isOpen() }),
    set(value = {}) {
    if (!alive) return Promise.resolve();
    if (typeof value.rail === 'boolean') setRail(value.rail, true);
    if (value.open === true) return openDrawer();
    if (value.open === false) return closeDrawer();
    return Promise.resolve();
    },
    on: events.on,
    destroy() {
    if (!alive) return;
    alive = false;
    abort.abort();
    events.clear();
    modal?.removeEventListener('change', onMedia);
    for (const handle of handles) handle?.destroy?.();
    prefs?.destroy();
    },
    };
    }
    define('shell', {
    attribute: 'data-ds-shell',
    init(el, ctx) {
    return frame(el, {
    key: el.getAttribute('data-ds-shell') ?? '',
    rail: el.getAttribute('data-ds-shell-rail') !== 'off',
    remember: el.getAttribute('data-ds-shell-remember') !== 'off',
    }, ctx.signal);
    },
    });
    function labelOf(node) {
    if (!node) return '';
    const label = node.querySelector(':scope > .ds-drawer__label, :scope > span:not(.ds-drawer__count, .ds-badge, .ds-sr)');
    return (label ?? node).textContent ?? '';
    }
    function groupKey(group) {
    return group.id || group.getAttribute('data-ds-key') || normalise(labelOf(group.querySelector(':scope > summary')));
    }
    function settleNow(element) {
    for (const animation of element.getAnimations?.({ subtree: true }) ?? []) {
    try { animation.finish(); } catch { }
    }
    }
    function drawer(nav, options = {}) {
    return navDrawer(nav, options, null);
    }
    function navDrawer(nav, options, signal) {
    const doc = nav.ownerDocument;
    const list = nav.querySelector('.ds-drawer__list') ?? nav;
    const field = (nav.querySelector('.ds-drawer__search input'));
    const prefs = prefsOf(options.remember);
    const key = `groups:${keyOf(options.key, nav.id || 'main')}`;
    const events = listeners(nav, signal);
    const abort = new AbortController();
    const on = { signal: abort.signal };
    const opened = new Set();
    let query = '';
    let count = 0;
    let timer = 0;
    const groups = () => [...nav.querySelectorAll('details.ds-drawer__group')];
    const memory = prefs?.get(key, {}) ?? {};
    for (const group of groups()) {
    const value = memory[groupKey(group)];
    if (typeof value === 'boolean') (group).open = value;
    }
    for (let group = nav.querySelector('[aria-current="page"]')?.closest('details.ds-drawer__group'); group && nav.contains(group); group = group.parentElement?.closest('details.ds-drawer__group')) {
    (group).open = true;
    }
    settleNow(nav);
    nav.addEventListener('toggle', (event) => {
    const group = (event.target);
    if (!prefs || query || !group.matches?.('details.ds-drawer__group')) return;
    prefs.set(key, { ...prefs.get(key, {}), [groupKey(group)]: group.open });
    }, { ...on, capture: true });
    let empty = (list.querySelector(':scope > .ds-drawer__empty'));
    const emptyNote = () => {
    if (!empty) {
    empty = doc.createElement('p');
    empty.className = 'ds-drawer__empty';
    empty.setAttribute(KEEP, '');
    empty.textContent = t('noResults');
    list.append(empty);
    }
    return empty;
    };
    const mark = marker('data-ds-filtered');
    const filterIn = (container, all) => {
    let shown = 0;
    for (const child of container.children) {
    if (child.matches('.ds-drawer__group')) {
    const heading = child.querySelector(':scope > .ds-drawer__heading');
    const headed = all || (Boolean(query) && Boolean(heading) && matchText(labelOf(heading), query));
    const inside = filterIn(child, headed);
    const show = !query || inside > 0 || headed;
    mark(child, !show);
    if (query && show && child.matches('details') && ! (child).open) {
    (child).open = true;
    own(child, 'attrs', 'open');
    opened.add(child);
    }
    shown += show ? inside : 0;
    } else if (child.matches('.ds-drawer__item, .ds-drawer__row')) {
    const item = child.matches('.ds-drawer__row') ? child.querySelector('.ds-drawer__item') : child;
    const show = !query || all || matchText(labelOf(item), query);
    mark(child, !show);
    if (show) shown += 1;
    }
    }
    return shown;
    };
    const apply = () => {
    count = filterIn(list, false);
    mark.release(list);
    if (!query) {
    for (const group of opened) {
    disown(group, 'attrs', 'open');
    (group).open = false;
    }
    opened.clear();
    }
    if (query && !count) {
    const note = emptyNote();
    mark(note, false);
    } else if (empty) mark(empty, true);
    };
    const filter = (value) => {
    const next = String(value ?? '');
    query = next.trim() ? next : '';
    apply();
    if (field && field.value !== next) field.value = next;
    emit(nav, 'filter', { query, count });
    clearTimeout(timer);
    if (!query) return;
    timer = setTimeout(() => {
    announce(count ? plural(count, { one: t('filterMatch'), other: t('filterMatches') }) : t('noResults'));
    }, motionDuration(nav, '--ds-delay-tooltip'));
    };
    field?.addEventListener('input', () => filter(field.value), on);
    const view = viewOf(nav);
    const changes = typeof view.MutationObserver === 'function' ? new view.MutationObserver(() => {
    if (!query) return;
    const before = count;
    apply();
    if (count !== before) emit(nav, 'filter', { query, count });
    }) : null;
    changes?.observe(list, { childList: true, subtree: true, characterData: true });
    const stops = () => [...nav.querySelectorAll('.ds-drawer__item, .ds-drawer__toggle')]
    .filter((node) => !node.closest('[data-ds-filtered]') && canTakeFocus(node));
    nav.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.isComposing) return;
    const target = (event.target);
    if (field && target === field) {
    if (event.key === 'Escape' && field.value) {
    event.preventDefault();
    filter('');
    } else if (event.key === 'ArrowDown') {
    const all = stops();
    const first = (query && all.find((node) => list.contains(node) && node.matches('.ds-drawer__item'))) || all.find((node) => list.contains(node)) || all[0];
    if (first) {
    event.preventDefault();
    (first).focus();
    }
    }
    return;
    }
    if (!target.matches?.('.ds-drawer__item, .ds-drawer__toggle')) return;
    const all = stops();
    const index = all.indexOf(target);
    if (index < 0) return;
    let next = null;
    if (event.key === 'ArrowDown') next = all[index + 1];
    else if (event.key === 'ArrowUp') next = all[index - 1] ?? (index === 0 ? field : null);
    else if (event.key === 'Home') next = all[0];
    else if (event.key === 'End') next = all.at(-1);
    if (!next || !canTakeFocus(next)) return;
    event.preventDefault();
    (next).focus();
    }, on);
    if (field?.value) filter(field.value);
    let alive = true;
    return {
    element: nav,
    get: () => ({ query, count, open: groups().filter((group) => (group).open).map(groupKey) }),
    set(value = {}) {
    if (!alive) return;
    if (typeof value.query === 'string') filter(value.query);
    if (value.open && typeof value.open === 'object') {
    for (const group of groups()) {
    const wanted = value.open[groupKey(group)];
    if (typeof wanted === 'boolean') (group).open = wanted;
    }
    }
    },
    on: events.on,
    destroy() {
    if (!alive) return;
    if (query) filter('');
    alive = false;
    clearTimeout(timer);
    changes?.disconnect();
    mark.release();
    abort.abort();
    events.clear();
    prefs?.destroy();
    empty?.remove();
    },
    };
    }
    define('drawer', {
    attribute: 'data-ds-drawer',
    init(el, ctx) {
    return navDrawer(el, { key: el.getAttribute('data-ds-drawer') ?? '', remember: el.getAttribute('data-ds-drawer-remember') !== 'off' }, ctx.signal);
    },
    });
    function around(doc, keep) {
    const out = [];
    const walk = (node) => {
    for (const child of node.children) {
    if (keep.includes(child)) continue;
    if (keep.some((element) => child.contains(element))) walk(child);
    else if (!child.matches('script, style, template, link, .ds-toasts, .ds-tooltip')) out.push(child);
    }
    };
    if (doc.body) walk(doc.body);
    return out;
    }
    function searchSheet(el, options = {}) {
    return sheet(el, options, null);
    }
    function sheet(el, options, signal) {
    const doc = el.ownerDocument;
    const phone = media(el, SEARCH_QUERY);
    const opener = options.opener ?? (el.closest('.ds-topbar') ?? doc).querySelector('.ds-topbar__search-open');
    const events = listeners(el, signal);
    const abort = new AbortController();
    const on = { signal: abort.signal };
    let still = (null);
    let locked = false;
    if (opener) {
    opener.setAttribute('aria-controls', idOf(el, 'search'));
    if (!opener.hasAttribute('aria-expanded')) opener.setAttribute('aria-expanded', 'false');
    }
    const isOpen = () => el.hasAttribute('data-state') && el.getAttribute('data-state') === 'open';
    const show = () => (phone?.matches ? openOverlay(el, { opener: opener ?? undefined }) : Promise.resolve());
    const hide = () => (el.hasAttribute('data-state') ? closeOverlay(el) : Promise.resolve());
    const engage = () => {
    if (still) return;
    still = inert(around(doc, [el, opener].filter(Boolean)));
    lockScroll(doc);
    locked = true;
    const input = (el.querySelector('input'));
    if (input && canTakeFocus(input)) input.focus({ preventScroll: true });
    };
    const release = () => {
    still?.destroy();
    still = null;
    if (locked) unlockScroll(doc);
    locked = false;
    };
    opener?.addEventListener('click', ( event) => {
    if (event.defaultPrevented) return;
    event.preventDefault();
    if (isOpen()) hide();
    else show();
    }, on);
    el.addEventListener('ds:open', (event) => { if (event.target === el) engage(); }, on);
    el.addEventListener('ds:close', (event) => { if (event.target === el) release(); }, on);
    el.addEventListener('keydown', ( event) => {
    const field = (event.target);
    if (event.key !== 'Escape' || !still || event.defaultPrevented || field.localName !== 'input' || !field.value) return;
    event.preventDefault();
    field.value = '';
    field.dispatchEvent(new (viewOf(el).Event)('input', { bubbles: true }));
    }, on);
    el.addEventListener('keydown', ( event) => {
    if (event.key !== 'Tab' || !still || event.defaultPrevented) return;
    const stops = tabbables(el);
    if (!stops.length) return;
    const active = doc.activeElement;
    const edge = event.shiftKey ? stops[0] : stops.at(-1);
    if (active !== edge) return;
    event.preventDefault();
    (event.shiftKey ? stops.at(-1) : stops[0]).focus();
    }, on);
    const onMedia = () => {
    if (phone?.matches) return;
    const back = () => { if (!phone?.matches) (el).hidden = false; };
    if (el.hasAttribute('data-state')) hide().then(back);
    else back();
    };
    phone?.addEventListener('change', onMedia);
    let alive = true;
    return {
    element: el,
    get: isOpen,
    set: (value) => (value ? show() : hide()),
    on: events.on,
    destroy() {
    if (!alive) return;
    alive = false;
    abort.abort();
    events.clear();
    phone?.removeEventListener('change', onMedia);
    release();
    },
    };
    }
    define('search-sheet', {
    attribute: 'data-ds-search-sheet',
    init: (el, ctx) => sheet(el, {}, ctx.signal),
    });
    function launcher(menu) {
    return grid(menu, null);
    }
    function grid(menu, signal) {
    const events = listeners(menu, signal);
    const abort = new AbortController();
    const apps = () => [...menu.querySelectorAll('.ds-menu__item')].filter(canTakeFocus);
    const columns = (items) => {
    const top = items[0]?.getBoundingClientRect().top ?? 0;
    const row = items.filter((item) => Math.abs(item.getBoundingClientRect().top - top) < 1).length;
    return Math.max(1, row);
    };
    menu.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    const items = apps();
    const index = items.indexOf( (event.target)?.closest?.('.ds-menu__item'));
    if (index < 0) return;
    const across = columns(items);
    const rtl = viewOf(menu).getComputedStyle?.(menu).direction === 'rtl';
    const step = { ArrowRight: rtl ? -1 : 1, ArrowLeft: rtl ? 1 : -1, ArrowDown: across, ArrowUp: -across }[event.key];
    let next = null;
    if (step !== undefined) next = items[index + step] ?? null;
    else if (event.key === 'Home') next = items[0];
    else if (event.key === 'End') next = items.at(-1);
    else return;
    event.preventDefault();
    if (next) (next).focus();
    }, { signal: abort.signal });
    let alive = true;
    return {
    element: menu,
    get: () => menu.querySelector('.ds-menu__item[aria-current]:not([aria-current="false"])'),
    set: (value) => (value ? openMenu(menu) : closeMenu(menu)),
    on: events.on,
    destroy() {
    if (!alive) return;
    alive = false;
    abort.abort();
    events.clear();
    },
    };
    }
    define('launcher', {
    attribute: 'data-ds-launcher',
    init: (el, ctx) => grid(el, ctx.signal),
    });
    function themeOf(from, target) {
    const doc = from?.ownerDocument ?? globalThis.document;
    if (target && typeof target === 'object') return target;
    if (typeof target === 'string' && target) return doc?.querySelector(target) ?? null;
    return from?.closest?.('.ds-theme') ?? doc?.querySelector('.ds-theme') ?? null;
    }
    function setScheme(value, options = {}) {
    if (!SCHEMES.has(value)) return null;
    const theme = themeOf(null, options.target);
    if (theme) {
    if (value === 'auto') theme.removeAttribute('data-ds-scheme');
    else theme.setAttribute('data-ds-scheme', value);
    }
    const doc = theme?.ownerDocument ?? globalThis.document;
    const link = typeof options.link === 'string' ? doc?.querySelector(options.link) : options.link;
    if (link) link.setAttribute('media', value === 'dark' ? 'all' : value === 'light' ? 'not all' : '(prefers-color-scheme: dark)');
    if (options.remember !== false) {
    const prefs = openStore(NAMESPACE);
    prefs.set(keyOf(options.key, 'scheme'), value);
    prefs.destroy();
    }
    return value;
    }
    function schemeSwitch(el, options = {}) {
    return switcher(el, options, null);
    }
    function switcher(el, options, signal) {
    const key = keyOf(options.key, 'scheme');
    const prefs = prefsOf(options.remember);
    const events = listeners(el, signal);
    const abort = new AbortController();
    const theme = () => themeOf(el, options.target);
    const items = () => [...el.querySelectorAll('[data-ds-scheme-value]')];
    const current = () => {
    const pinned = theme()?.getAttribute('data-ds-scheme');
    return pinned === 'light' || pinned === 'dark' ? pinned : 'auto';
    };
    let value = current();
    const radios = () => items().filter((item) => item.getAttribute('role') === 'radio' && !item.hidden && !item.matches(':disabled'));
    const check = () => {
    for (const item of items()) item.setAttribute('aria-checked', String(item.getAttribute('data-ds-scheme-value') === value));
    const set = radios();
    const stop = set.find((item) => item.getAttribute('aria-checked') === 'true') ?? set[0];
    for (const item of set) item.setAttribute('tabindex', item === stop ? '0' : '-1');
    };
    const apply = (next, remember) => {
    if (!SCHEMES.has(next)) return;
    const changed = next !== value;
    value = next;
    setScheme(next, { target: theme() ?? undefined, link: options.link, key, remember: remember && options.remember !== false });
    check();
    if (changed) emit(el, 'change', { value });
    };
    const stored = prefs?.get(key);
    if (SCHEMES.has(stored)) apply(stored, false);
    else check();
    el.addEventListener('click', ( event) => {
    const item = (event.target)?.closest?.('[data-ds-scheme-value]');
    if (!item || !el.contains(item) || item.getAttribute('aria-disabled') === 'true') return;
    apply(item.getAttribute('data-ds-scheme-value') ?? '', true);
    }, { signal: abort.signal });
    el.addEventListener('keydown', ( event) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    const set = radios();
    const item = set.find((each) => each === event.target);
    if (!item) return;
    if (event.key === ' ' && item.localName !== 'button') {
    event.preventDefault();
    item.click();
    return;
    }
    const rtl = viewOf(el).getComputedStyle?.(el).direction === 'rtl';
    const step = { ArrowDown: 1, ArrowUp: -1, ArrowRight: rtl ? -1 : 1, ArrowLeft: rtl ? 1 : -1 }[event.key];
    if (step === undefined) return;
    event.preventDefault();
    const usable = set.filter((each) => each === item || each.getAttribute('aria-disabled') !== 'true');
    const next = usable[(usable.indexOf(item) + step + usable.length) % usable.length];
    if (!next || next === item) return;
    next.focus();
    apply(next.getAttribute('data-ds-scheme-value') ?? '', true);
    }, { signal: abort.signal });
    const off = prefs?.on('change', (detail) => {
    if (detail.key === key && SCHEMES.has(detail.value) && detail.value !== value) apply(detail.value, false);
    });
    const view = viewOf(el);
    const watch = typeof view.MutationObserver === 'function' ? new view.MutationObserver(() => {
    const now = current();
    if (now === value) return;
    value = now;
    check();
    emit(el, 'change', { value });
    }) : null;
    const watched = theme();
    if (watched) watch?.observe(watched, { attributes: true, attributeFilter: ['data-ds-scheme'] });
    let alive = true;
    return {
    element: el,
    get: () => value,
    set: (next) => apply(next, true),
    on: events.on,
    destroy() {
    if (!alive) return;
    alive = false;
    abort.abort();
    events.clear();
    off?.();
    watch?.disconnect();
    prefs?.destroy();
    },
    };
    }
    define('scheme-switch', {
    attribute: 'data-ds-scheme-switch',
    init(el, ctx) {
    return switcher(el, {
    key: el.getAttribute('data-ds-scheme-switch') ?? '',
    target: el.getAttribute('data-ds-scheme-switch-target') ?? undefined,
    link: el.getAttribute('data-ds-scheme-switch-link') ?? undefined,
    remember: el.getAttribute('data-ds-scheme-switch-remember') !== 'off',
    }, ctx.signal);
    },
    });
    function fitCrumbs(widths, room, more) {
    const n = widths.length;
    const total = widths.reduce((sum, width) => sum + width, 0);
    if (n < 2 || total <= room) return [];
    const order = [];
    for (let i = 1; i < n - 2; i += 1) order.push(i);
    order.push(0);
    if (n > 2) order.push(n - 2);
    const folded = [];
    let width = total + more;
    for (const index of order) {
    if (width <= room) break;
    folded.push(index);
    width -= widths[index];
    }
    return folded.sort((a, b) => a - b);
    }
    function breadcrumbs(nav) {
    return trail(nav, null);
    }
    function trail(nav, signal) {
    const doc = nav.ownerDocument;
    const view = viewOf(nav);
    const ol = nav.querySelector('ol');
    const events = listeners(nav, signal);
    const abort = new AbortController();
    if (!ol) return { element: nav, get: () => [], set() {}, on: events.on, destroy() { events.clear(); } };
    const more = doc.createElement('li');
    more.className = 'ds-breadcrumbs__more';
    more.setAttribute(KEEP, '');
    const collapse = marker('data-ds-collapsed');
    collapse(more, true);
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-xs';
    button.setAttribute('aria-haspopup', 'menu');
    button.setAttribute('aria-expanded', 'false');
    button.append(iconElement(moreHorizontal, 20, '', doc));
    const menu = doc.createElement('div');
    menu.className = 'ds-menu';
    menu.setAttribute('role', 'menu');
    menu.hidden = true;
    menu.setAttribute(KEEP, '');
    button.setAttribute('aria-controls', idOf(menu, 'crumbs'));
    more.append(button);
    nav.after(menu);
    const crumbs = () => [...ol.children].filter((li) => li !== more && li.localName === 'li');
    let folded = ([]);
    const fill = () => {
    const rows = folded.map((li) => {
    const link = li.querySelector('a[href]');
    const row = doc.createElement('a');
    row.className = 'ds-menu__item';
    row.setAttribute('role', 'menuitem');
    row.href = link?.getAttribute('href') ?? '#';
    const label = doc.createElement('span');
    label.className = 'ds-menu__label';
    label.textContent = (link ?? li).textContent?.trim() ?? '';
    row.append(label);
    return row;
    });
    menu.replaceChildren(...rows);
    const label = t('breadcrumbsMore', { count: folded.length });
    button.setAttribute('aria-label', label);
    button.setAttribute('data-tip', label);
    menu.setAttribute('aria-label', label);
    };
    const measure = () => {
    if (!nav.isConnected) return;
    const list = crumbs();
    if (more.parentNode !== ol || more.previousElementSibling !== (list[0] ?? null)) list[0]?.after(more);
    for (const li of list) collapse(li, false);
    collapse.release(ol);
    collapse(more, false);
    ol.style.setProperty('inline-size', 'max-content');
    const widths = list.map((li) => li.getBoundingClientRect().width);
    const moreWidth = more.getBoundingClientRect().width;
    ol.style.removeProperty('inline-size');
    const room = nav.getBoundingClientRect().width;
    const hide = fitCrumbs(widths, room, moreWidth);
    const next = hide.map((index) => list[index]);
    for (const li of next) collapse(li, true);
    if (!next.length) {
    collapse(more, true);
    if (button.getAttribute('aria-expanded') === 'true') closeMenu(menu);
    }
    const changed = next.length !== folded.length || next.some((li, i) => li !== folded[i]);
    folded = next;
    if (changed) {
    fill();
    emit(nav, 'change', { folded: folded.length });
    }
    };
    let pending = 0;
    const schedule = () => {
    if (pending) return;
    const run = () => { pending = 0; measure(); };
    pending = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(run) : 0;
    if (!pending) run();
    };
    button.addEventListener('click', ( event) => {
    if (event.detail > 1) return;
    if (button.getAttribute('aria-expanded') === 'true') closeMenu(menu);
    else openMenu(menu, { opener: button });
    }, { signal: abort.signal });
    button.addEventListener('keydown', ( event) => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    openMenu(menu, { opener: button });
    }, { signal: abort.signal });
    const resize = typeof view.ResizeObserver === 'function' ? new view.ResizeObserver(schedule) : null;
    resize?.observe(nav);
    if (!resize) view.addEventListener?.('resize', schedule, { signal: abort.signal });
    const changes = typeof view.MutationObserver === 'function' ? new view.MutationObserver(schedule) : null;
    changes?.observe(ol, { childList: true, subtree: true, characterData: true });
    doc.addEventListener('ds:config', () => { if (folded.length) fill(); }, { signal: abort.signal });
    measure();
    let alive = true;
    return {
    element: nav,
    get: () => [...folded],
    set: () => measure(),
    on: events.on,
    destroy() {
    if (!alive) return;
    alive = false;
    abort.abort();
    events.clear();
    resize?.disconnect();
    changes?.disconnect();
    if (pending) view.cancelAnimationFrame?.(pending);
    collapse.release();
    more.remove();
    if (button.getAttribute('aria-expanded') === 'true') closeMenu(menu).then(() => menu.remove());
    else menu.remove();
    },
    };
    }
    define('breadcrumbs', {
    attribute: 'data-ds-breadcrumbs',
    init: (el, ctx) => trail(el, ctx.signal),
    });

    return Object.freeze({
      SEARCH_QUERY: SEARCH_QUERY,
      matchText: matchText,
      create: create,
      drawer: drawer,
      searchSheet: searchSheet,
      launcher: launcher,
      setScheme: setScheme,
      schemeSwitch: schemeSwitch,
      fitCrumbs: fitCrumbs,
      breadcrumbs: breadcrumbs
    });
  });

  add('toast', ['toastRegion', 'announce', 'toast', 'closeToasts', 'promise', 'progress'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const t = DS.core.t;
    const flip = DS.motion.flip, settled = DS.motion.settled;
    const alertCircle = DS.icons.alertCircle, checkCircle = DS.icons.checkCircle, closeGlyph = DS.icons.close, iconElement = DS.icons.iconElement, info = DS.icons.info;
    const setShare = DS.dom.progress;
    const percent = DS.format.percent;
    const pxOf = DS.position.pxOf, round = DS.position.round;
    const internals = DS.overlay.internals, TOAST_LIMIT = DS.overlay.TOAST_LIMIT, TOAST_MAX_MS = DS.overlay.TOAST_MAX_MS, TOAST_MS = DS.overlay.TOAST_MS;
    const { overlays, stack, neverInert, shownSavebars, LANDMARK, activeIn, inside, own, disown, viewOf, flushStyle, canFocus, report, keepAway, keepHome, clock, normalText, timeScaleOf, nextFrame } = internals;
    const TOAST_CHAR_MS = 50;
    const TOAST_FREE_CHARS = 40;
    const PHONE_QUERY = '(max-width: 600px)';
    const toastRegions = new WeakMap();
    const toastEntries = new WeakMap();
    const pointedRegions = (new Set());
    const pointerWatched = new WeakSet();
    const visibilityRegions = new WeakMap();
    const drawerRegions = new WeakMap();
    const toastHomes = new WeakMap();
    const hostedRegions = new Map();
    const followRuns = new WeakMap();
    function regionState(region) {
    let state = toastRegions.get(region);
    if (!state) {
    state = {
    shown: [], queue: [], pointer: false, focus: false, origin: null,
    polite: null, assertive: null, pending: new Map(),
    reserved: null, observer: null, lifted: false, expanded: false,
    };
    toastRegions.set(region, state);
    for (const name of ['role', 'aria-live']) {
    if (!region.hasAttribute(name)) continue;
    region.removeAttribute(name);
    own(region, 'attrs', name);
    }
    wireRegion(region, state);
    hostLate(region);
    }
    return state;
    }
    function hostLate(region) {
    const theme = region.closest?.('.ds-theme') ?? null;
    for (const host of [...stack]) {
    const state = overlays.get(host);
    if (state?.phase !== 'open' || (state.kind !== 'dialog' && state.kind !== 'sheet')) continue;
    const hostTheme = host.closest('.ds-theme');
    if (hostTheme && hostTheme !== theme) continue;
    followInto(region, host);
    }
    }
    function appOf(region) {
    const scope = region.closest?.('.ds-theme') ?? region.ownerDocument;
    return (scope?.querySelector?.('.ds-app') ?? null);
    }
    const paused = (region, state) => state.pointer || state.focus || state.expanded || Boolean(region.ownerDocument.hidden);
    function limitOf(region) {
    const given = Number.parseInt(region.getAttribute?.('data-ds-toast-limit') ?? '', 10);
    return Number.isInteger(given) ? Math.min(5, Math.max(1, given)) : TOAST_LIMIT;
    }
    const stacks = (region) => Boolean(region.classList?.contains('ds-toasts--stack'));
    function arm(entry, region, state) {
    if (entry.stays || entry.phase !== 'shown' || entry.timer || paused(region, state)) return;
    entry.started = clock();
    entry.timer = setTimeout(() => {
    entry.timer = 0;
    closeToast(entry);
    }, Math.max(0, entry.remaining));
    }
    function disarm(entry) {
    if (!entry.timer) return;
    clearTimeout(entry.timer);
    entry.timer = 0;
    entry.remaining -= clock() - entry.started;
    }
    function pauseToasts(state) {
    for (const entry of state.shown) disarm(entry);
    }
    function resumeToasts(region, state) {
    for (const entry of state.shown) arm(entry, region, state);
    }
    function unpoint(region, state) {
    pointedRegions.delete(region);
    if (!state.pointer) return;
    state.pointer = false;
    resumeToasts(region, state);
    }
    function wireRegion(region, state) {
    const doc = region.ownerDocument;
    region.addEventListener('pointerover', ( event) => {
    if (state.pointer || region.contains( (event.relatedTarget))) return;
    state.pointer = true;
    pointedRegions.add(region);
    pauseToasts(state);
    });
    region.addEventListener('pointerout', ( event) => {
    if (region.contains( (event.relatedTarget))) return;
    unpoint(region, state);
    });
    region.addEventListener('focusin', ( event) => {
    const from = (event.relatedTarget);
    if (!state.focus && from && !region.contains(from)) state.origin = from;
    state.focus = true;
    pauseToasts(state);
    expandStack(region, state, true);
    });
    region.addEventListener('focusout', ( event) => {
    if (region.contains( (event.relatedTarget))) return;
    state.focus = false;
    if (!state.pointer) expandStack(region, state, false);
    resumeToasts(region, state);
    });
    region.addEventListener('click', ( event) => {
    const target = (event.target);
    if (!state.expanded && stacks(region) && target.closest?.('.ds-toast') && !target.closest('button, a[href], input, select, textarea')) {
    expandStack(region, state, true);
    return;
    }
    const button = target.closest?.('.ds-toast__close, .ds-toast__action');
    const entry = button && region.contains(button) ? toastEntries.get(button.closest('.ds-toast')) : null;
    if (!entry || entry.phase !== 'shown') return;
    if (button.classList.contains('ds-toast__close')) {
    closeToast(entry);
    return;
    }
    const run = entry.onAction;
    if (!run) return;
    entry.onAction = null;
    closeToast(entry);
    try {
    run(event, entry.handle);
    } catch (error) {
    report(region, error);
    }
    });
    const regions = visibilityOf(doc);
    for (const ref of [...regions]) if (!ref.deref()) regions.delete(ref);
    regions.add(new WeakRef(region));
    watchPointer(doc);
    watchPresses(doc);
    const app = appOf(region);
    const lifted = app ? drawerOf(app) : null;
    if (lifted) {
    for (const ref of [...lifted]) if (!ref.deref()) lifted.delete(ref);
    lifted.add(new WeakRef(region));
    }
    }
    function drawerOf(app) {
    let watched = drawerRegions.get(app);
    if (watched) return watched;
    const Observer = viewOf(app).MutationObserver;
    if (typeof Observer !== 'function') return null;
    const regions = (new Set());
    drawerRegions.set(app, regions);
    new Observer(() => {
    for (const ref of [...regions]) {
    const region = ref.deref();
    const state = region && toastRegions.get(region);
    if (!state) regions.delete(ref);
    else if (region.isConnected && state.shown.length) liftRegion(region);
    }
    }).observe(app, { attributes: true, attributeFilter: ['data-drawer-open'] });
    return regions;
    }
    function watchPointer(doc) {
    if (pointerWatched.has(doc)) return;
    pointerWatched.add(doc);
    doc.addEventListener('pointerover', ( event) => {
    for (const pointed of [...pointedRegions]) {
    if (!inside(pointed, event)) unpoint(pointed, toastRegions.get(pointed));
    }
    }, true);
    }
    const pressWatched = new WeakSet();
    const expandedRegions = (new Set());
    function watchPresses(doc) {
    if (pressWatched.has(doc)) return;
    pressWatched.add(doc);
    doc.addEventListener('pointerdown', ( event) => {
    for (const region of [...expandedRegions]) {
    const state = toastRegions.get(region);
    if (!state || !region.isConnected) expandedRegions.delete(region);
    else if (!inside(region, event)) expandStack(region, state, false);
    }
    }, true);
    }
    function expandStack(region, state, on) {
    const next = on && stacks(region) && state.shown.filter((entry) => entry.phase === 'shown').length > 1;
    if (next === state.expanded) return;
    state.expanded = next;
    if (next) expandedRegions.add(region);
    else expandedRegions.delete(region);
    flip(region, () => {
    region.toggleAttribute('data-expanded', next);
    stackFront(region, state);
    }).then(() => reserveRoom(region, state));
    if (next) pauseToasts(state);
    else resumeToasts(region, state);
    }
    function stackFront(region, state) {
    const style = (region).style;
    if (!stacks(region)) {
    if (style.getPropertyValue('--ds-toast-front')) style.removeProperty('--ds-toast-front');
    return;
    }
    const front = [...state.shown].reverse().find((entry) => entry.phase === 'shown' && entry.el.parentNode === region);
    if (!front) {
    style.removeProperty('--ds-toast-front');
    return;
    }
    style.setProperty('--ds-toast-front', `${Math.ceil( (front.el).offsetHeight)}px`);
    }
    function visibilityOf(doc) {
    let watched = visibilityRegions.get(doc);
    if (watched) return watched;
    const regions = (new Set());
    visibilityRegions.set(doc, regions);
    doc.addEventListener('visibilitychange', () => {
    for (const ref of [...regions]) {
    const region = ref.deref();
    const state = region && toastRegions.get(region);
    if (!state) regions.delete(ref);
    else if (doc.hidden) pauseToasts(state);
    else resumeToasts(region, state);
    }
    });
    return regions;
    }
    function closeButton(doc, label) {
    const button = doc.createElement('button');
    button.className = 'ds-btn ds-btn--quiet ds-btn--icon ds-toast__close';
    button.type = 'button';
    const name = label || t('close');
    button.setAttribute('aria-label', name);
    button.setAttribute('data-tip', name);
    button.append(iconElement(closeGlyph, 20, undefined, doc));
    return button;
    }
    function buildToast(doc, text, kind, closable, closeLabel, actionLabel) {
    const el = doc.createElement('div');
    el.className = kind === 'error' || kind === 'info' ? `ds-toast ds-toast--${kind}` : 'ds-toast';
    el.append(iconElement(kind === 'error' ? alertCircle : kind === 'info' ? info : checkCircle, 20, undefined, doc));
    const span = doc.createElement('span');
    span.className = 'ds-toast__text';
    span.textContent = text;
    el.append(span);
    if (actionLabel) {
    const action = doc.createElement('button');
    action.className = 'ds-btn ds-btn--text ds-sm ds-toast__action';
    action.type = 'button';
    action.textContent = actionLabel;
    el.append(action);
    }
    if (closable) el.append(closeButton(doc, closeLabel));
    return el;
    }
    function announcers(region, state) {
    const live = (key) => {
    const node = state[key];
    return Boolean(node?.isConnected) && node.classList.contains('ds-sr') && node.getAttribute('aria-live') === key;
    };
    if (live('polite') && live('assertive')) return;
    const doc = region.ownerDocument;
    for (const key of (['polite', 'assertive'])) {
    if (live(key)) continue;
    if (state[key]) neverInert.delete(state[key]);
    const node = doc.createElement('div');
    node.className = 'ds-sr';
    node.setAttribute('aria-live', key);
    node.setAttribute('aria-atomic', 'true');
    node.setAttribute('data-ds-keep', '');
    neverInert.add(node);
    state[key] = node;
    }
    region.after( (state.polite), (state.assertive));
    }
    function speak(region, state, entry) {
    if (!entry.text) return;
    announcers(region, state);
    const node = (entry.assertive ? state.assertive : state.polite);
    const texts = state.pending.get(node);
    if (texts) {
    const at = entry.key ? texts.findIndex((item) => item.key === entry.key) : -1;
    if (at >= 0) texts.splice(at, 1);
    texts.push({ text: entry.text, key: entry.key });
    return;
    }
    state.pending.set(node, [{ text: entry.text, key: entry.key }]);
    node.textContent = '';
    nextFrame(region.ownerDocument, () => {
    node.textContent = (state.pending.get(node) ?? []).map((item) => item.text).join('\n');
    state.pending.delete(node);
    });
    }
    function toastTimeout(region, text, options) {
    const given = typeof options.timeout === 'number' && options.timeout > 0 ? options.timeout : 0;
    const base = given || Math.min(TOAST_MAX_MS, TOAST_MS + TOAST_CHAR_MS * Math.max(0, text.length - TOAST_FREE_CHARS));
    return base * timeScaleOf(region);
    }
    function toastRegion(doc = globalThis.document) {
    if (!doc?.querySelector) return null;
    const app = doc.querySelector('.ds-app');
    const theme = app?.closest('.ds-theme') ?? null;
    const found = (theme && ownOf(theme, '.ds-toasts')) ?? doc.querySelector('.ds-toasts');
    if (found) return (found);
    return newRegion(doc, app, doc.querySelector('.ds-theme') ?? doc.body);
    }
    function ownOf(theme, selector) {
    return [...theme.querySelectorAll(selector)].find((node) => node.closest('.ds-theme') === theme) ?? null;
    }
    function newRegion(doc, app, parent) {
    const region = doc.createElement('div');
    region.className = 'ds-toasts';
    if (app) app.after(region);
    else (parent === doc.documentElement && doc.body ? doc.body : parent)?.append(region);
    return region.isConnected ? region : null;
    }
    function regionOfTheme(doc, theme, home) {
    if (!theme) return toastRegion(doc);
    return (ownOf(theme, '.ds-toasts')) ?? newRegion(doc, ownOf(theme, '.ds-app'), home);
    }
    function announce(text, options = {}) {
    const value = String(text ?? '').replace(/\s+/g, ' ').trim();
    if (!value) return;
    const region = options.region ?? toastRegion();
    if (!region?.ownerDocument) return;
    speak(region, regionState(region), { text: value, assertive: options.assertive === true, key: typeof options.key === 'string' && options.key ? options.key : '' });
    }
    function toast(region, content, options = {}) {
    const target = region ?? (content == null ? null : toastRegion());
    const doc = target?.ownerDocument;
    if (!doc || content == null) return { el: null, close: () => Promise.resolve(), closed: Promise.resolve() };
    const state = regionState(target);
    const given = typeof content === 'object' && (content).nodeType === 1;
    const element = (given ? content : null);
    const kind = options.kind ?? (element?.matches('.ds-toast--error, .ds-tone-error') ? 'error' : element?.matches('.ds-toast--info, .ds-tone-info') ? 'info' : 'success');
    const label = !given && options.action ? String(options.action.label ?? '').trim() : '';
    const persistent = options.persistent === true || kind === 'error' || Boolean(label);
    const el = element ?? buildToast(doc, String(content), kind, persistent, options.closeLabel, label);
    const action = [...el.querySelectorAll('button, a[href]')].some((node) => !node.classList.contains('ds-toast__close'));
    if (given && persistent && !action && !el.querySelector('.ds-toast__close')) el.append(closeButton(doc, options.closeLabel));
    const text = ((el.querySelector('.ds-toast__text') ?? el).textContent ?? '').replace(/\s+/g, ' ').trim();
    if (/^(alert|status)$/.test(el.getAttribute('role') ?? '')) el.removeAttribute('role');
    el.removeAttribute('aria-live');
    const explicit = options.key != null && options.key !== '';
    const key = explicit ? String(options.key) : !given && !label && options.restart !== false ? `restart:${kind}:${persistent}:${normalText(text)}` : null;
    const match = key ? [...state.shown, ...state.queue].find((other) => other.key === key && (other.phase === 'shown' || other.phase === 'queued')) : null;
    if (match && (!explicit || match.el === el)) {
    if (match.phase === 'shown') restartToast(match, target, state);
    return match.handle;
    }
    const timeout = toastTimeout(target, text, options);
    let done = () => {};
    const closed = new Promise((resolve) => {
    done = () => resolve(undefined);
    });
    const stays = persistent || action || !Number.isFinite(timeout);
    const entry = {
    el, region: target, kind, key, stays, reserve: stays && options.reserve !== false, assertive: kind === 'error', text,
    timeout, remaining: timeout, timer: 0, started: 0, phase: 'queued', done, closed,
    onAction: label && typeof options.action?.onClick === 'function' ? options.action.onClick : null,
    handle: ( (null)),
    };
    entry.handle = { el, close: () => closeToast(entry), closed };
    toastEntries.set(el, entry);
    if (match) replaceToast(target, state, match, entry);
    else {
    state.queue.push(entry);
    updateToasts(target, state, []);
    }
    return entry.handle;
    }
    function closeToasts(region, filter = {}) {
    const target = region ?? toastRegion();
    const state = target ? toastRegions.get(target) : null;
    if (!state) return Promise.resolve();
    const matches = (entry) => (filter.kind == null || entry.kind === filter.kind) && (filter.key == null || entry.key === String(filter.key));
    const queued = state.queue.filter(matches);
    const shown = state.shown.filter((entry) => entry.phase === 'shown' && matches(entry));
    for (const entry of queued) {
    state.queue.splice(state.queue.indexOf(entry), 1);
    finishToast(entry);
    }
    if (shown.length) updateToasts(target, state, shown);
    return Promise.all([...queued, ...shown].map((entry) => entry.closed)).then(() => undefined);
    }
    function outcomeText(given, value) {
    const text = typeof given === 'function' ? given(value) : given;
    return text == null || text === false ? '' : String(text).replace(/\s+/g, ' ').trim();
    }
    function buildPending(doc, text, bar) {
    const el = doc.createElement('div');
    el.className = bar ? 'ds-toast ds-toast--progress' : 'ds-toast';
    const spinner = doc.createElement('span');
    spinner.className = 'ds-spinner ds-toast__spinner';
    spinner.setAttribute('aria-hidden', 'true');
    const span = doc.createElement('span');
    span.className = 'ds-toast__text';
    span.textContent = text;
    if (!bar) {
    el.append(spinner, span);
    return el;
    }
    const body = doc.createElement('div');
    body.className = 'ds-toast__body';
    const meter = doc.createElement('div');
    meter.className = 'ds-progress ds-progress--flat';
    const track = doc.createElement('div');
    track.className = 'ds-progress__track';
    track.setAttribute('role', 'progressbar');
    track.setAttribute('aria-valuemin', '0');
    track.setAttribute('aria-valuemax', '100');
    const fill = doc.createElement('div');
    fill.className = 'ds-progress__bar';
    track.append(fill);
    meter.append(track);
    body.append(span, meter);
    const value = doc.createElement('span');
    value.className = 'ds-toast__value';
    value.setAttribute('aria-hidden', 'true');
    el.append(body, value);
    return el;
    }
    function settleToast(entry, kind, text, options = {}) {
    const { el, region } = entry;
    const state = toastRegions.get(region);
    if (!state) return;
    if (entry.phase === 'gone' || entry.phase === 'leaving') {
    if (text) speak(region, state, { text, assertive: kind === 'error' });
    return;
    }
    if (!text) {
    closeToast(entry);
    return;
    }
    const doc = el.ownerDocument;
    const change = () => {
    const glyph = iconElement(kind === 'error' ? alertCircle : kind === 'info' ? info : checkCircle, 20, undefined, doc);
    const old = el.querySelector(':scope > .ds-toast__spinner, :scope > svg');
    if (old) old.replaceWith(glyph);
    else el.prepend(glyph);
    const span = el.querySelector('.ds-toast__text');
    if (span) {
    span.textContent = text;
    span.removeAttribute('id');
    if (span.parentElement !== el) el.querySelector(':scope > .ds-toast__body')?.replaceWith(span);
    }
    el.querySelector(':scope > .ds-toast__value')?.remove();
    el.classList.remove('ds-toast--progress', 'ds-toast--error', 'ds-toast--info');
    if (kind === 'error' || kind === 'info') el.classList.add(`ds-toast--${kind}`);
    el.removeAttribute('aria-busy');
    };
    if (entry.phase === 'shown' && el.parentNode === region) {
    flip(region, () => {
    change();
    stackFront(region, state);
    }).then(() => reserveRoom(region, state));
    } else change();
    entry.kind = kind;
    entry.text = text;
    entry.assertive = kind === 'error';
    entry.stays = kind === 'error' || options.persistent === true;
    entry.reserve = entry.stays && options.reserve !== false;
    disarm(entry);
    entry.timeout = toastTimeout(region, text, options);
    entry.remaining = entry.timeout;
    if (entry.phase === 'shown') {
    speak(region, state, entry);
    arm(entry, region, state);
    }
    }
    function promise(region, work, options = {}) {
    const target = region ?? toastRegion();
    const doc = target?.ownerDocument;
    let running;
    try {
    running = Promise.resolve(typeof work === 'function' ? work() : work);
    } catch (error) {
    running = Promise.reject(error);
    }
    if (!doc) {
    running.catch(() => {});
    return { el: null, close: () => Promise.resolve(), closed: Promise.resolve() };
    }
    const el = buildPending(doc, outcomeText(options.pending, undefined) || t('loading'), false);
    el.setAttribute('aria-busy', 'true');
    const handle = toast(target, el, { ...options, kind: 'info', persistent: true, restart: false });
    const entry = toastEntries.get(el);
    if (!entry) return handle;
    entry.kind = 'pending';
    running.then(
    (value) => settleToast(entry, 'success', outcomeText(options.success === undefined ? t('done') : options.success, value), options),
    (reason) => settleToast(entry, 'error', outcomeText(options.error === undefined ? t('actionFailed') : options.error, reason), options),
    );
    return handle;
    }
    let progressCount = 0;
    function progress(region, text, options = {}) {
    const target = region ?? toastRegion();
    const doc = target?.ownerDocument;
    const idle = { el: null, close: () => Promise.resolve(), closed: Promise.resolve(), set() {}, done() {}, fail() {} };
    if (!doc) return idle;
    const el = buildPending(doc, String(text ?? '').replace(/\s+/g, ' ').trim(), true);
    const span = (el.querySelector('.ds-toast__text'));
    const track = (el.querySelector('.ds-progress__track'));
    const figure = (el.querySelector('.ds-toast__value'));
    const meter = (el.querySelector('.ds-progress'));
    progressCount += 1;
    span.id = `ds-toast-progress-${progressCount}`;
    track.setAttribute('aria-labelledby', span.id);
    const show = (share) => {
    const known = typeof share === 'number' && Number.isFinite(share);
    const clamped = known ? Math.min(1, Math.max(0, share)) : null;
    setShare(meter, clamped);
    figure.textContent = clamped === null ? '' : percent(clamped, { maximumFractionDigits: 0 });
    };
    show(options.value ?? null);
    const handle = toast(target, el, { ...options, kind: 'info', persistent: true, restart: false });
    const entry = toastEntries.get(el);
    if (!entry) return { ...idle, ...handle };
    entry.kind = 'pending';
    let ended = false;
    return {
    ...handle,
    set(share, next) {
    if (ended) return;
    show(share);
    if (typeof next === 'string' && next.trim()) {
    span.textContent = next.replace(/\s+/g, ' ').trim();
    entry.text = span.textContent;
    }
    },
    done(next, more = {}) {
    if (ended) return;
    ended = true;
    setShare(meter, 1);
    settleToast(entry, 'success', outcomeText(next ?? t('done'), undefined), { ...options, ...more });
    },
    fail(next) {
    if (ended) return;
    ended = true;
    settleToast(entry, 'error', outcomeText(next ?? t('actionFailed'), undefined), options);
    },
    };
    }
    function closeToast(entry) {
    const state = toastRegions.get(entry.region);
    if (!state) return entry.closed;
    if (entry.phase === 'queued') {
    state.queue.splice(state.queue.indexOf(entry), 1);
    finishToast(entry);
    } else if (entry.phase === 'shown') updateToasts(entry.region, state, [entry]);
    return entry.closed;
    }
    function finishToast(entry) {
    if (entry.phase === 'gone') return;
    entry.phase = 'gone';
    disarm(entry);
    toastEntries.delete(entry.el);
    entry.done();
    }
    function restartToast(entry, region, state) {
    disarm(entry);
    entry.remaining = entry.timeout;
    arm(entry, region, state);
    speak(region, state, entry);
    }
    function replaceToast(region, state, old, entry) {
    if (old.phase === 'queued') {
    state.queue.splice(state.queue.indexOf(old), 1, entry);
    finishToast(old);
    return;
    }
    const focused = old.el.contains(activeIn(old.el));
    disarm(old);
    old.phase = 'leaving';
    state.shown.splice(state.shown.indexOf(old), 1, entry);
    entry.phase = 'shown';
    const moved = flip(region, (hooks) => {
    region.insertBefore(entry.el, old.el.parentNode === region ? old.el : null);
    const leaving = hooks.onRemove(old.el);
    if (leaving) leaving.then(() => finishToast(old));
    else {
    old.el.remove();
    finishToast(old);
    }
    });
    if (focused) {
    const button = entry.el.querySelector('.ds-toast__close');
    if (canFocus(button)) (button).focus({ preventScroll: true });
    else releaseToastFocus(old, region, state, state.shown.filter((other) => other !== entry));
    }
    shownNow(region, state, [entry], moved);
    }
    function releaseToastFocus(entry, region, state, staying) {
    const doc = region.ownerDocument;
    if (!entry.el.contains(activeIn(entry.el))) return;
    const others = staying.filter((other) => other.el.parentNode === region);
    const after = others.filter((other) => entry.el.compareDocumentPosition(other.el) & 4);
    const before = others.filter((other) => !after.includes(other)).reverse();
    const buttons = [...after, ...before].map((other) => other.el.querySelector('.ds-toast__close'));
    const origin = state.origin && !region.contains(state.origin) ? state.origin : null;
    const view = region.closest('[tabindex="-1"]') ?? doc.querySelector(LANDMARK);
    const target = [...buttons, origin, view].find((node) => canFocus(node) && !entry.el.contains(node));
    target?.focus({ preventScroll: true });
    }
    function updateToasts(region, state, closing) {
    const next = state.shown.filter((entry) => !closing.includes(entry));
    const added = [];
    const limit = limitOf(region);
    while (state.queue.length) {
    if (next.length >= limit) {
    const oldest = next.findIndex((entry) => !entry.stays);
    if (oldest < 0) break;
    next.splice(oldest, 1);
    }
    const entry = (state.queue.shift());
    next.push(entry);
    added.push(entry);
    }
    const removed = state.shown.filter((entry) => !next.includes(entry));
    const entering = added.filter((entry) => next.includes(entry));
    for (const entry of added) if (!next.includes(entry)) finishToast(entry);
    if (!removed.length && !entering.length) return;
    for (const entry of removed) {
    disarm(entry);
    entry.phase = 'leaving';
    }
    if (!state.shown.length && entering.length) writeLift(region, liftOf(region));
    state.shown = next;
    for (const entry of removed) releaseToastFocus(entry, region, state, next.filter((other) => !entering.includes(other)));
    const moved = flip(region, (hooks) => {
    for (const entry of removed) {
    const leaving = hooks.onRemove(entry.el);
    if (leaving) leaving.then(() => finishToast(entry));
    else {
    entry.el.remove();
    finishToast(entry);
    }
    }
    for (const entry of entering) {
    entry.phase = 'shown';
    region.append(entry.el);
    }
    stackFront(region, state);
    });
    if (state.expanded && next.length < 2) expandStack(region, state, false);
    shownNow(region, state, entering, moved);
    }
    function shownNow(region, state, entering, moved) {
    const landed = entering.map((entry) => {
    speak(region, state, entry);
    arm(entry, region, state);
    const played = settled(entry.el);
    played.then(() => {
    if (entry.phase !== 'shown') return;
    own(entry.el, 'classes', 'ds-toast--shown');
    entry.el.classList.add('ds-toast--shown');
    });
    return played;
    });
    Promise.all([moved, ...landed]).then(() => reserveRoom(region, state));
    }
    function hostOf(region) {
    const homes = toastHomes.get(region);
    const last = homes?.[homes.length - 1];
    return last && !last.leaving ? last.host : null;
    }
    function reserveTarget(region) {
    const host = hostOf(region);
    const node = (host
    ? (host.matches('.ds-dialog') ? host : host.querySelector('.ds-sheet__body'))
    : appOf(region)?.querySelector('.ds-view') ?? null);
    return node?.getAttribute('data-ds-toast-room') === 'off' ? null : node;
    }
    function releaseReserve(state) {
    state.observer?.disconnect();
    state.observer = null;
    const node = state.reserved;
    if (!node) return;
    state.reserved = null;
    node.removeAttribute('data-toast-reserve');
    node.style.removeProperty('--ds-toast-reserve');
    disown(node, 'attrs', 'data-toast-reserve');
    disown(node, 'props', '--ds-toast-reserve');
    }
    function reserveRoom(region, state) {
    const staying = state.shown.filter((entry) => entry.phase === 'shown' && entry.reserve && entry.el.parentNode === region);
    const node = staying.length && region.isConnected ? reserveTarget(region) : null;
    if (state.reserved && state.reserved !== node) releaseReserve(state);
    if (!node) return;
    const top = Math.min(...staying.map((entry) => entry.el.getBoundingClientRect().top));
    let room = 0;
    if (node.matches('.ds-dialog')) {
    const box = node.querySelector('.ds-dialog__box') ?? node;
    const applied = state.reserved === node ? parseFloat(node.style.getPropertyValue('--ds-toast-reserve')) || 0 : 0;
    if (box.getBoundingClientRect().bottom + applied / 2 <= top) {
    releaseReserve(state);
    return;
    }
    const doc = region.ownerDocument;
    room = (doc.documentElement.clientHeight || viewOf(region).innerHeight) - top + pxOf(region, '--ds-space-3', 12);
    } else room = node.getBoundingClientRect().bottom - top;
    if (state.reserved !== node) {
    own(node, 'attrs', 'data-toast-reserve');
    own(node, 'props', '--ds-toast-reserve');
    state.reserved = node;
    }
    node.setAttribute('data-toast-reserve', '');
    node.style.setProperty('--ds-toast-reserve', `${Math.ceil(Math.max(0, room))}px`);
    const Observer = viewOf(region).ResizeObserver;
    if (!state.observer && typeof Observer === 'function') {
    let first = true;
    state.observer = new Observer(() => {
    if (first) first = false;
    else reserveRoom(region, state);
    });
    state.observer.observe(region);
    }
    if (node.matches('.ds-dialog')) return;
    for (const el of new Set([activeIn(node), state.origin])) {
    if (!el || el === node || !node.contains(el) || typeof el.scrollIntoView !== 'function') continue;
    if (el.getBoundingClientRect().bottom > top) el.scrollIntoView({ block: 'nearest' });
    }
    }
    function liftOf(region) {
    const gap = pxOf(region, '--ds-space-2', 8);
    const above = (node) => (node && node.offsetHeight > 0 ? node.offsetHeight + gap : 0);
    const host = hostOf(region);
    if (host) return host.matches('.ds-sheet') ? above(host.querySelector('.ds-sheet__foot')) : 0;
    let lift = 0;
    const app = appOf(region);
    if (app?.hasAttribute('data-drawer-open') && viewOf(region).matchMedia?.(PHONE_QUERY).matches) lift = above(app.querySelector('.ds-drawer .ds-drawer__foot'));
    const theme = region.closest('.ds-theme');
    for (const bar of shownSavebars) if (bar.element.closest('.ds-theme') === theme) lift = Math.max(lift, bar.lift(region));
    return lift;
    }
    function writeLift(region, px) {
    const state = regionState(region);
    const style = (region).style;
    if (px > 0.5) {
    style.setProperty('--ds-toast-lift', `${round(px)}px`);
    if (!state.lifted) own(region, 'props', '--ds-toast-lift');
    state.lifted = true;
    } else {
    style.removeProperty('--ds-toast-lift');
    if (state.lifted) disown(region, 'props', '--ds-toast-lift');
    state.lifted = false;
    }
    }
    function liftRegion(region) {
    const next = liftOf(region);
    const now = parseFloat( (region).style.getPropertyValue('--ds-toast-lift')) || 0;
    if (Math.abs(next - now) < 0.5) return;
    const state = regionState(region);
    glideRegion(region, next > now ? 'enter' : 'exit', () => writeLift(region, next)).then(() => reserveRoom(region, state));
    }
    function moveRegion(region, parent, before) {
    const state = toastRegions.get(region);
    const active = activeIn(region);
    const refocus = active && region.contains(active) ? active : null;
    const nodes = [region, state?.polite, state?.assertive].filter((node) => node && node.parentNode === region.parentNode);
    const anchor = before && before.parentNode === parent ? before : null;
    for (const node of nodes) {
    if (typeof parent.moveBefore === 'function') {
    try {
    parent.moveBefore(node, anchor);
    continue;
    } catch {
    }
    }
    parent.insertBefore(node, anchor);
    }
    if (refocus && activeIn(refocus) !== refocus && canFocus(refocus)) refocus.focus({ preventScroll: true });
    }
    function glideRegion(region, direction, move) {
    const style = (region).style;
    const run = (followRuns.get(region) ?? 0) + 1;
    followRuns.set(region, run);
    if (![...region.children].some((child) => child.matches('.ds-toast:not([data-ds-leaving])'))) {
    style.removeProperty('--ds-toast-shift');
    style.removeProperty('--ds-toast-shift-y');
    move();
    region.removeAttribute('data-follow');
    return Promise.resolve();
    }
    const transition = style.transition;
    const from = region.getBoundingClientRect();
    style.transition = 'none';
    style.removeProperty('--ds-toast-shift');
    style.removeProperty('--ds-toast-shift-y');
    move();
    const to = region.getBoundingClientRect();
    const dx = from.left - to.left;
    const dy = from.bottom - to.bottom;
    if (Math.abs(dx) < 0.5 && Math.abs(dy) < 0.5) {
    flushStyle(region);
    style.transition = transition;
    region.removeAttribute('data-follow');
    return Promise.resolve();
    }
    style.setProperty('--ds-toast-shift', `${round(dx)}px`);
    style.setProperty('--ds-toast-shift-y', `${round(dy)}px`);
    flushStyle(region);
    region.setAttribute('data-follow', direction);
    style.transition = transition;
    style.removeProperty('--ds-toast-shift');
    style.removeProperty('--ds-toast-shift-y');
    return settled(region, { transitions: true }).then(() => {
    if (followRuns.get(region) === run) region.removeAttribute('data-follow');
    });
    }
    function hostToasts(host, step) {
    const doc = host.ownerDocument;
    const theme = host.closest('.ds-theme');
    const scope = !theme || theme === doc.documentElement ? doc : theme;
    const regions = new Set([...scope.querySelectorAll('.ds-toasts')].filter((region) => !theme || region.closest('.ds-theme') === theme));
    if (step !== 'enter') for (const region of hostedRegions.keys()) regions.add(region);
    for (const region of regions) {
    if (step === 'enter') followInto(region, host);
    else if (step === 'exit') followOut(region, host);
    else giveRegionBack(region, host);
    }
    }
    function hostedLeft(region, host) {
    const panel = host.matches('.ds-sheet') ? host.querySelector('.ds-sheet__panel') : null;
    if (!panel) return NaN;
    return host.getBoundingClientRect().left + host.clientLeft + (panel).offsetLeft + pxOf(region, '--ds-space-6', 24);
    }
    function roomAfter(region, glide) {
    const state = toastRegions.get(region);
    if (!state) return;
    reserveRoom(region, state);
    glide.then(() => reserveRoom(region, state));
    }
    function followInto(region, host) {
    const homes = toastHomes.get(region) ?? [];
    const own = homes.find((home) => home.host === host);
    if (own) {
    if (own.leaving) {
    own.leaving = false;
    const left = hostedLeft(region, host);
    roomAfter(region, glideRegion(region, 'enter', () => {
    if (Number.isFinite(left)) (region).style.setProperty('--ds-toast-left', `${round(left)}px`);
    writeLift(region, liftOf(region));
    }));
    }
    return;
    }
    if (host.contains(region)) return;
    if (host.matches('.ds-sheet') && !host.querySelector('.ds-sheet__panel')) return;
    const state = toastRegions.get(region);
    let next = region.nextSibling;
    while (next && (next === state?.polite || next === state?.assertive)) next = next.nextSibling;
    const at = parseFloat(viewOf(region).getComputedStyle?.(region).left ?? '');
    homes.push({ host, parent: (region.parentNode), next, left: (region).style.getPropertyValue('--ds-toast-left'), at, leaving: false });
    toastHomes.set(region, homes);
    if (!hostedRegions.has(region)) {
    hostedRegions.set(region, [region, state?.polite, state?.assertive].filter((node) => node && node.parentNode === region.parentNode && keepAway(node)));
    }
    const sheetLeft = hostedLeft(region, host);
    const left = Number.isFinite(sheetLeft) ? sheetLeft : at;
    roomAfter(region, glideRegion(region, 'enter', () => {
    moveRegion(region, host, null);
    if (Number.isFinite(left)) (region).style.setProperty('--ds-toast-left', `${round(left)}px`);
    if (state) writeLift(region, liftOf(region));
    }));
    }
    function passHome(homes, index) {
    const home = homes[index];
    Object.assign(homes[index + 1], { parent: home.parent, next: home.next, left: home.left, at: home.at });
    homes.splice(index, 1);
    }
    function followOut(region, host) {
    const homes = toastHomes.get(region);
    const index = homes ? homes.findIndex((home) => home.host === host) : -1;
    if (!homes || index < 0) return;
    if (index < homes.length - 1) {
    passHome(homes, index);
    if (homes[homes.length - 1].leaving) glideHome(region, homes[homes.length - 1]);
    return;
    }
    homes[index].leaving = true;
    glideHome(region, homes[index]);
    }
    function glideHome(region, home) {
    const lift = toastRegions.has(region) ? liftOf(region) : 0;
    roomAfter(region, glideRegion(region, 'exit', () => {
    if (Number.isFinite(home.at)) (region).style.setProperty('--ds-toast-left', `${round(home.at)}px`);
    if (toastRegions.has(region)) writeLift(region, lift);
    }));
    }
    function releaseToasts(host) {
    const doc = host.ownerDocument;
    for (const region of new Set([...host.querySelectorAll('.ds-toasts'), ...doc.querySelectorAll('.ds-toasts')])) {
    const homes = toastHomes.get(region);
    const index = homes ? homes.findIndex((home) => home.host === host) : -1;
    if (index < 0) continue;
    if (index < homes.length - 1) {
    passHome(homes, index);
    continue;
    }
    homes[index].leaving = true;
    giveRegionBack(region, host, false);
    }
    }
    function giveRegionBack(region, host, glide = true) {
    const homes = toastHomes.get(region);
    const index = homes ? homes.findIndex((home) => home.host === host && home.leaving) : -1;
    if (!homes || index < 0) return;
    if (index < homes.length - 1) return passHome(homes, index);
    const home = homes[index];
    homes.pop();
    if (!homes.length) {
    toastHomes.delete(region);
    for (const node of hostedRegions.get(region) ?? []) keepHome(node, true);
    hostedRegions.delete(region);
    }
    const parent = home.parent.isConnected ? home.parent : host.parentNode;
    if (!parent) return;
    const move = () => {
    moveRegion(region, parent, home.next);
    const style = (region).style;
    if (home.left) style.setProperty('--ds-toast-left', home.left);
    else style.removeProperty('--ds-toast-left');
    if (toastRegions.has(region)) writeLift(region, liftOf(region));
    };
    roomAfter(region, glide ? glideRegion(region, 'exit', move) : (move(), Promise.resolve()));
    }
    internals.use('toast', {
    toast,
    announce,
    closeToasts,
    toastRegion,
    regionOfTheme,
    host: hostToasts,
    release: releaseToasts,
    lift: liftRegion,
    hosted: (region) => toastHomes.has(region),
    });

    return Object.freeze({
      toastRegion: toastRegion,
      announce: announce,
      toast: toast,
      closeToasts: closeToasts,
      promise: promise,
      progress: progress
    });
  });

  add('tooltip', ['tooltips'], function () {
    /*! Z-Dash 3.0.1 | MIT License | https://github.com/saschazesiger/Z-Dash */
    const define = DS.core.define;
    const motionDuration = DS.motion.motionDuration, originFrom = DS.motion.originFrom, settled = DS.motion.settled;
    const claimAnchor = DS.position.claimAnchor, dropProbe = DS.position.dropProbe, positionPlace = DS.position.place, pxOf = DS.position.pxOf, releaseAnchor = DS.position.releaseAnchor, supportsAnchoring = DS.position.supportsAnchoring;
    const internals = DS.overlay.internals;
    const { tooltipControllers, neverInert, activeIn, sharedOf, own, disown, viewOf, flushStyle, watchModality, layerOver, opensOpenLayer, nextAnchorName, clock, normalText, nextFrame } = internals;
    let tooltipWarned = false;
    function accessibleName(node) {
    const doc = node.ownerDocument;
    const ids = (node.getAttribute('aria-labelledby') ?? '').split(/\s+/).filter(Boolean);
    if (ids.length) return ids.map((id) => doc.getElementById(id)?.textContent ?? '').join(' ');
    return node.getAttribute('aria-label') ?? node.textContent ?? '';
    }
    const TAP_SLOP_PX = 10;
    const CUT_DESCENDANTS = 24;
    const CUT_ANCESTORS = 3;
    function visibleText(trigger) {
    const doc = trigger.ownerDocument;
    const verdicts = new Map();
    const hidden = (element) => {
    for (let at = element; at && at !== trigger.parentElement; at = at.parentElement) {
    if (verdicts.has(at)) return verdicts.get(at);
    const box = at.getBoundingClientRect?.();
    const gone = at.matches('.ds-sr, [aria-hidden="true"], svg, svg *') || !at.getClientRects?.().length || (box && box.width <= 1 && box.height <= 1);
    if (gone) {
    verdicts.set(at, true);
    return true;
    }
    if (at === trigger) break;
    }
    verdicts.set(element, false);
    return false;
    };
    const walker = doc.createTreeWalker(trigger, 4);
    const parts = [];
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const parent = node.parentElement;
    if (parent && node.nodeValue?.trim() && !hidden(parent)) parts.push(node.nodeValue);
    }
    return normalText(parts.join(' '));
    }
    function clips(element) {
    const style = viewOf(element).getComputedStyle?.(element);
    if (!style || (style.overflowX === 'visible' && style.overflowY === 'visible')) return false;
    return element.scrollWidth > element.clientWidth + 1 || element.scrollHeight > element.clientHeight + 1;
    }
    function tipWanted(trigger) {
    const tip = normalText(trigger.getAttribute('data-tip'));
    if (!tip) return null;
    if (normalText(trigger.getAttribute('data-tip-shortcut'))) return 'adds';
    const text = visibleText(trigger);
    if (text !== tip) return 'adds';
    if (clips(trigger)) return 'cut';
    const descendants = trigger.querySelectorAll('*');
    for (let index = 0; index < descendants.length && index < CUT_DESCENDANTS; index += 1) if (clips(descendants[index])) return 'cut';
    let at = trigger.parentElement;
    for (let step = 0; at && step < CUT_ANCESTORS && normalText(at.textContent) === text; step += 1, at = at.parentElement) {
    if (clips(at)) return 'cut';
    }
    return null;
    }
    const SIDES = new Set(['bottom', 'top', 'start', 'end']);
    function sideOf(trigger) {
    const side = trigger.getAttribute('data-tip-placement') ?? '';
    return SIDES.has(side) ? side : 'bottom';
    }
    function focusVisible(node) {
    try {
    return node.matches(':focus-visible');
    } catch {
    return true;
    }
    }
    function tooltips(root = globalThis.document) {
    const doc = root?.nodeType === 9 ? (root) : root?.ownerDocument;
    if (!doc) return { destroy() {} };
    const view = doc.defaultView ?? globalThis;
    const shadow = root.nodeType === 11;
    const rootElement = root.nodeType === 9 ? doc.documentElement : (root);
    const roots = sharedOf(doc).tipRoots;
    const existing = roots.get(rootElement);
    if (existing) return existing;
    watchModality(doc);
    const theme = shadow ? (root).querySelector('.ds-theme')
    : rootElement.closest('.ds-theme') ?? (root.nodeType === 9 ? doc.querySelector('.ds-theme') : null);
    if (!theme && !tooltipWarned) {
    tooltipWarned = true;
    view.console?.warn('Z-Dash: DS.overlay.tooltips() on a root outside .ds-theme. The tooltip must sit inside the .ds-theme element, or it gets neither the tokens nor the motion.');
    }
    const anchored = supportsAnchoring(view);
    const home = !theme || theme === doc.documentElement ? doc.body : theme;
    const tip = doc.createElement('div');
    tip.className = 'ds-tooltip';
    tip.setAttribute('role', 'tooltip');
    tip.id = `ds-tooltip-${sharedOf(doc).seq += 1}`;
    tip.hidden = true;
    tip.setAttribute('data-state', 'closed');
    if (anchored) {
    tip.setAttribute('popover', 'manual');
    tip.setAttribute('data-anchored', '');
    }
    neverInert.add(tip);
    tip.setAttribute('data-ds-keep', '');
    home.append(tip);
    let phase = 'hidden';
    let trigger = (null);
    let hovering = false;
    let focusing = false;
    let showTimer = (0);
    let hideTimer = (0);
    let lastHide = -Infinity;
    let run = 0;
    let frame = 0;
    let ownAnchor = (null);
    let described = (null);
    let press = (null);
    let touchShown = false;
    let swallow = (null);
    const Observer = view.MutationObserver;
    const watcher = typeof Observer === 'function' ? new Observer(() => {
    if (phase !== 'hidden' && trigger && !trigger.isConnected) hideNow();
    }) : null;
    let watching = false;
    const time = (token) => motionDuration(tip.isConnected ? tip : shadow ? theme ?? (root).host : rootElement, token);
    function delayOf(trigger) {
    const delay = trigger.getAttribute('data-tip-delay');
    if (delay === 'none') return 0;
    if (delay === 'short') return time('--ds-delay-tooltip-skip');
    if (delay === 'long') return 2 * time('--ds-delay-tooltip');
    return time('--ds-delay-tooltip');
    }
    const contains = (node) => (root.nodeType === 9 ? doc.contains(node) : rootElement.contains(node));
    function hostFor(node) {
    return node.closest('dialog[open], .ds-dialog:not(.ds-dialog--contained), .ds-sheet') ?? home;
    }
    function anchorTo(next) {
    if (!anchored || (ownAnchor && ownAnchor === next)) return;
    unanchor();
    const { name, mine } = claimAnchor(next, () => nextAnchorName(doc));
    if (mine) ownAnchor = (next);
    tip.style.setProperty('position-anchor', name);
    }
    function unanchor() {
    releaseAnchor(ownAnchor);
    ownAnchor = null;
    }
    function readSide(next) {
    const box = tip.getBoundingClientRect();
    const anchor = next.getBoundingClientRect();
    const rtl = view.getComputedStyle?.(next).direction === 'rtl';
    let side = 'bottom';
    if (box.bottom <= anchor.top + 1) side = 'top';
    else if (box.right <= anchor.left + 1) side = rtl ? 'end' : 'start';
    else if (box.left >= anchor.right - 1) side = rtl ? 'start' : 'end';
    if (side === (tip.getAttribute('data-placement') ?? 'bottom')) return false;
    if (side === 'bottom') tip.removeAttribute('data-placement');
    else tip.setAttribute('data-placement', side);
    return true;
    }
    function place(next) {
    const side = sideOf(next);
    if (side === 'bottom') tip.removeAttribute('data-side');
    else tip.setAttribute('data-side', side);
    if (anchored) {
    readSide(next);
    const mine = run;
    nextFrame(doc, () => nextFrame(doc, () => {
    if (mine !== run || phase !== 'open' || trigger !== next || !tip.isConnected) return;
    if (readSide(next)) originFrom(next, tip);
    }));
    return;
    }
    const gap = pxOf(tip, '--ds-space-2', 8);
    positionPlace(tip, next, { side, align: 'center', offset: gap, padding: gap, flip: 'fits', size: false, x: '--ds-tooltip-x', y: '--ds-tooltip-y' });
    }
    function describe(next, why) {
    undescribe();
    if (why === 'cut' || normalText(tip.textContent) === normalText(accessibleName(next))) return;
    const ids = (next.getAttribute('aria-describedby') ?? '').split(/\s+/).filter(Boolean);
    if (!ids.includes(tip.id)) next.setAttribute('aria-describedby', [...ids, tip.id].join(' '));
    own(next, 'attrs', 'aria-describedby');
    described = next;
    }
    function undescribe() {
    if (!described) return;
    const ids = (described.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((id) => id && id !== tip.id);
    if (ids.length) described.setAttribute('aria-describedby', ids.join(' '));
    else described.removeAttribute('aria-describedby');
    disown(described, 'attrs', 'aria-describedby');
    described = null;
    }
    function cancelTimers() {
    clearTimeout(showTimer);
    clearTimeout(hideTimer);
    showTimer = 0;
    hideTimer = 0;
    }
    function show(next, touch = false) {
    cancelTimers();
    const text = next.getAttribute('data-tip') ?? '';
    const why = next.isConnected && !opensOpenLayer(next) ? tipWanted(next) : null;
    if (!text.trim() || !why) return;
    touchShown = touch;
    if (watcher && !watching) {
    watcher.observe(shadow ? (root) : rootElement, { childList: true, subtree: true });
    watching = true;
    }
    const host = hostFor(next);
    const visible = phase !== 'hidden' && tip.parentNode === host;
    const warm = !visible && clock() - lastHide < time('--ds-delay-tooltip-skip');
    run += 1;
    trigger = next;
    focusing = next === activeIn(next) && focusVisible(next);
    tip.textContent = text;
    const shortcut = next.getAttribute('data-tip-shortcut');
    if (shortcut) {
    const slot = doc.createElement('span');
    slot.className = 'ds-tooltip__shortcut';
    slot.textContent = shortcut;
    tip.append(slot);
    }
    if (visible) {
    anchorTo(next);
    place(next);
    originFrom(next, tip);
    tip.setAttribute('data-state', 'open');
    } else {
    if (tip.parentNode !== host) {
    try {
    if (anchored && tip.matches(':popover-open')) tip.hidePopover();
    } catch {
    }
    host.append(tip);
    }
    tip.setAttribute('data-state', 'closed');
    tip.setAttribute('data-warm', '');
    tip.hidden = false;
    anchorTo(next);
    if (anchored) {
    try {
    tip.showPopover();
    } catch {
    }
    }
    place(next);
    originFrom(next, tip);
    tip.style.transition = 'none';
    if (!warm) tip.removeAttribute('data-warm');
    flushStyle(tip);
    tip.style.removeProperty('transition');
    tip.setAttribute('data-state', 'open');
    }
    phase = 'open';
    describe(next, why);
    }
    function unwatch() {
    if (!watching) return;
    watcher?.disconnect();
    watching = false;
    }
    function rest() {
    phase = 'hidden';
    trigger = null;
    touchShown = false;
    try {
    if (anchored && tip.matches(':popover-open')) tip.hidePopover();
    } catch {
    }
    tip.hidden = true;
    unanchor();
    dropProbe(tip);
    }
    function hide() {
    cancelTimers();
    if (phase !== 'open') return;
    phase = 'closing';
    lastHide = clock();
    undescribe();
    unwatch();
    const mine = ++run;
    tip.setAttribute('data-state', 'closed');
    settled(tip, { transitions: true }).then(() => {
    if (mine !== run || phase !== 'closing') return;
    rest();
    });
    }
    function hideNow() {
    cancelTimers();
    if (phase === 'hidden') return;
    if (phase === 'open') lastHide = clock();
    undescribe();
    unwatch();
    run += 1;
    hovering = false;
    focusing = false;
    tip.style.transition = 'none';
    tip.setAttribute('data-state', 'closed');
    flushStyle(tip);
    tip.style.removeProperty('transition');
    rest();
    }
    function hideLater() {
    clearTimeout(showTimer);
    showTimer = 0;
    if (hovering || focusing || phase !== 'open' || hideTimer) return;
    hideTimer = setTimeout(() => {
    hideTimer = 0;
    if (!hovering && !focusing) hide();
    }, time('--ds-delay-tooltip-hide'));
    }
    const triggerOf = (node) => {
    const found = (node?.closest?.('[data-tip]') ?? null);
    if (!found || !contains(found)) return null;
    for (let at = (found); at; at = at.parentElement) {
    if (roots.has(at)) return at === rootElement ? found : null;
    }
    return found;
    };
    function onPointerOver( event) {
    if (event.pointerType === 'touch') return;
    const next = triggerOf(event.target);
    if (!next || next.contains( (event.relatedTarget))) return;
    if (!tipWanted(next)) return;
    hovering = true;
    clearTimeout(hideTimer);
    hideTimer = 0;
    if (phase === 'open' && trigger === next) return;
    if (phase !== 'hidden' || clock() - lastHide < time('--ds-delay-tooltip-skip')) {
    show(next);
    return;
    }
    clearTimeout(showTimer);
    const wait = delayOf(next);
    if (wait <= 0) {
    show(next);
    return;
    }
    showTimer = setTimeout(() => {
    showTimer = 0;
    show(next);
    }, wait);
    }
    function onPointerOut( event) {
    if (event.pointerType === 'touch') return;
    const from = triggerOf(event.target);
    const to = (event.relatedTarget);
    if (!from || from.contains(to) || tip.contains(to)) return;
    hovering = false;
    hideLater();
    }
    function onTipOver( event) {
    if (event.pointerType === 'touch' || phase !== 'open') return;
    hovering = true;
    clearTimeout(hideTimer);
    hideTimer = 0;
    }
    function onTipOut( event) {
    if (event.pointerType === 'touch') return;
    const to = (event.relatedTarget);
    if (tip.contains(to) || trigger?.contains(to)) return;
    hovering = false;
    hideLater();
    }
    function onFocusIn( event) {
    const next = triggerOf(event.target);
    if (!next || !focusVisible(next) || !tipWanted(next)) return;
    hovering = false;
    show(next);
    }
    function onFocusOut( event) {
    const from = triggerOf(event.target);
    if (!from || from !== trigger || from.contains( (event.relatedTarget))) return;
    focusing = false;
    hideLater();
    }
    function onKeyDown( event) {
    if (event.key !== 'Escape' || event.defaultPrevented || !escape()) return;
    event.preventDefault();
    event.stopPropagation();
    }
    const origin = ( event) => (event.composedPath?.()[0] ?? event.target);
    const tapShows = (node) => node.matches('[role="img"], [aria-disabled="true"]');
    function cancelPress() {
    if (press) clearTimeout(press.timer);
    press = null;
    }
    function unswallow() {
    if (!swallow) return;
    doc.removeEventListener('click', swallow, true);
    doc.removeEventListener('contextmenu', swallow, true);
    swallow = null;
    }
    function swallowNext() {
    unswallow();
    swallow = ( event) => {
    event.preventDefault();
    event.stopImmediatePropagation();
    if (event.type === 'click') unswallow();
    };
    doc.addEventListener('click', swallow, true);
    doc.addEventListener('contextmenu', swallow, true);
    }
    function onPointerDown( event) {
    clearTimeout(showTimer);
    showTimer = 0;
    unswallow();
    cancelPress();
    const next = event.pointerType === 'touch' ? triggerOf(origin(event)) : null;
    const hidesOwn = Boolean(next) && phase === 'open' && touchShown && trigger === next;
    hide();
    if (!next || hidesOwn) return;
    press = { trigger: next, id: event.pointerId, x: event.clientX, y: event.clientY, timer: 0 };
    if (tapShows(next) || next.matches('a[href]')) return;
    press.timer = setTimeout(() => {
    if (!press || press.trigger !== next) return;
    press = null;
    show(next, true);
    if (phase === 'open' && trigger === next) swallowNext();
    }, time('--ds-delay-tooltip'));
    }
    function onPointerMove( event) {
    if (!press || event.pointerId !== press.id) return;
    if (Math.hypot(event.clientX - press.x, event.clientY - press.y) > TAP_SLOP_PX) cancelPress();
    }
    function onPointerUp( event) {
    if (!press || event.pointerId !== press.id) return;
    const next = press.trigger;
    const near = Math.hypot(event.clientX - press.x, event.clientY - press.y) <= TAP_SLOP_PX;
    cancelPress();
    if (near && tapShows(next) && triggerOf(origin(event)) === next) show(next, true);
    }
    function onScroll() {
    if (phase === 'open' && touchShown) {
    hide();
    return;
    }
    if (phase !== 'open' || frame) return;
    const update = () => {
    frame = 0;
    if (phase !== 'open' || !trigger) return;
    const rect = trigger.getBoundingClientRect();
    const gone = !trigger.isConnected || rect.bottom <= 0 || rect.right <= 0 || rect.top >= view.innerHeight || rect.left >= view.innerWidth;
    if (gone) hide();
    else if (!anchored) place(trigger);
    };
    frame = typeof view.requestAnimationFrame === 'function' ? view.requestAnimationFrame(update) : 0;
    if (!frame) update();
    }
    function escape() {
    if (phase !== 'open') return false;
    const under = Boolean(trigger?.isConnected && layerOver(trigger));
    hovering = false;
    focusing = false;
    hide();
    return !under;
    }
    const controller = {
    hide() {
    clearTimeout(showTimer);
    showTimer = 0;
    hide();
    },
    escape,
    };
    tooltipControllers.add(controller);
    const target = (root);
    target.addEventListener('pointerover', onPointerOver);
    target.addEventListener('pointerout', onPointerOut);
    target.addEventListener('focusin', onFocusIn);
    target.addEventListener('focusout', onFocusOut);
    tip.addEventListener('pointerover', onTipOver);
    tip.addEventListener('pointerout', onTipOut);
    doc.addEventListener('keydown', onKeyDown, true);
    doc.addEventListener('pointerdown', onPointerDown, true);
    doc.addEventListener('pointermove', onPointerMove, { capture: true, passive: true });
    doc.addEventListener('pointerup', onPointerUp, true);
    doc.addEventListener('pointercancel', cancelPress, true);
    doc.addEventListener('scroll', onScroll, { capture: true, passive: true });
    view.addEventListener('resize', onScroll);
    const handle = {
    destroy() {
    if (roots.get(rootElement) === handle) roots.delete(rootElement);
    tooltipControllers.delete(controller);
    cancelTimers();
    if (frame && typeof view.cancelAnimationFrame === 'function') view.cancelAnimationFrame(frame);
    target.removeEventListener('pointerover', onPointerOver);
    target.removeEventListener('pointerout', onPointerOut);
    target.removeEventListener('focusin', onFocusIn);
    target.removeEventListener('focusout', onFocusOut);
    doc.removeEventListener('keydown', onKeyDown, true);
    doc.removeEventListener('pointerdown', onPointerDown, true);
    doc.removeEventListener('pointermove', onPointerMove, { capture: true });
    doc.removeEventListener('pointerup', onPointerUp, true);
    doc.removeEventListener('pointercancel', cancelPress, true);
    doc.removeEventListener('scroll', onScroll, { capture: true });
    view.removeEventListener('resize', onScroll);
    cancelPress();
    unswallow();
    unwatch();
    undescribe();
    unanchor();
    dropProbe(tip);
    run += 1;
    phase = 'hidden';
    tip.remove();
    },
    };
    roots.set(rootElement, handle);
    return handle;
    }
    define('tooltips', {
    global(root) {
    const doc = root.nodeType === 9 ? root : root.ownerDocument;
    const existed = Boolean(doc) && sharedOf(doc).tipRoots.has(root.nodeType === 9 ? doc.documentElement : root);
    const handle = tooltips(root);
    return existed ? () => {} : () => handle.destroy();
    },
    });
    internals.use('tooltip', { tooltips });

    return Object.freeze({
      tooltips: tooltips
    });
  });

  // DS.core's entry points at the top level; a copy already on the page keeps its own.
  ['init', 'get', 'define', 'config'].forEach(function (key) {
    if (DS[key] === undefined && DS.core[key] !== undefined) DS[key] = DS.core[key];
  });
  // window.DS and the warning are the first copy's business.
  if (first) return;
  versions[version] = DS;
  var current = root.DS;
  var warn = function (message) {
    if (root.console && typeof root.console.warn === 'function') root.console.warn(message);
  };
  if (current === undefined || current === null || (typeof current === 'object' && current.version === version)) root.DS = DS;
  else if (typeof current.version === 'string') warn('Z-Dash ' + version + ' loaded on a page that already runs ' + current.version + ': window.DS stays ' + current.version + '; this copy is ZDash.versions[\'' + version + '\'].');
  else warn('Z-Dash ' + version + ': window.DS already holds something else; it is left alone, this copy is ZDash.versions[\'' + version + '\'].');
})(typeof globalThis !== 'undefined' ? globalThis : this);

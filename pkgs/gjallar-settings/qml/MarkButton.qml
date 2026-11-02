import QtQuick
import QtQuick.Controls

// One change to an app, a Wine app's network or a protection (Plane 276).
// A click marks the change and the button turns into Undo; nothing runs
// before Apply in the bar at the bottom.
Button {
    required property var backend
    required property string kind
    required property string itemId
    // The state the system has now.
    required property bool value
    property string onText: "Turn on"
    property string offText: "Turn off"
    // What the change is about, for screen readers.
    property string label: ""
    property bool allowed: true
    readonly property bool marked: backend.changes[kind + ":" + itemId] !== undefined

    text: marked ? "Undo" : (value ? offText : onText)
    flat: marked
    enabled: allowed && !backend.applying
    Accessible.name: marked ? "Undo the change to " + label : text + " " + label
    onClicked: marked ? backend.undo(kind, itemId) : backend.mark(kind, itemId, !value)
}

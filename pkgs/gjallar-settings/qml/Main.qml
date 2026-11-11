import QtQuick
import QtQuick.Controls
import QtQuick.Controls.impl
import QtQuick.Layouts

// One column, a back arrow, swipe right to go back (Plane 218, Q91).
// Changes are marked on the pages and run together from the bar at the
// bottom (Plane 276).
ApplicationWindow {
    id: window

    required property var backend
    required property string startPage
    // An installer to set up as a new Wine app, as a file:// URL, or "".
    required property string wineInstaller
    required property var colours

    width: 560
    height: 720
    minimumWidth: 360
    minimumHeight: 420
    visible: true
    title: stack.currentItem && stack.currentItem.title ? stack.currentItem.title : "Settings"
    color: palette.window

    readonly property int gap: 12
    // Show-look easing: cubic-bezier(0.16, 1, 0.3, 1), 500 ms.
    readonly property var showCurve: [0.16, 1, 0.3, 1, 1, 1]

    function openPage(name) {
        if (name === "security") {
            stack.push(securityPage)
        } else if (name === "apps") {
            stack.push(appsPage)
        }
    }

    header: ToolBar {
        background: Rectangle { color: palette.window }
        RowLayout {
            anchors.fill: parent
            anchors.leftMargin: window.gap / 2
            anchors.rightMargin: window.gap
            ToolButton {
                icon.name: "go-previous"
                text: "Back"
                display: AbstractButton.IconOnly
                visible: stack.depth > 1
                onClicked: stack.pop()
                Accessible.name: "Back"
            }
            Label {
                textFormat: Text.PlainText
                text: window.title
                font.pixelSize: 20
                font.weight: Font.DemiBold
                Layout.fillWidth: true
                Layout.leftMargin: stack.depth > 1 ? 0 : window.gap
                elide: Label.ElideRight
            }
            BusyIndicator {
                running: window.backend.busy
                visible: running
                implicitWidth: 28
                implicitHeight: 28
            }
        }
    }

    footer: ToolBar {
        visible: window.backend.changeCount > 0 || window.backend.applying
        background: Rectangle { color: palette.alternateBase }
        RowLayout {
            anchors.fill: parent
            anchors.leftMargin: window.gap
            anchors.rightMargin: window.gap
            spacing: window.gap / 2
            Label {
                text: window.backend.applying ? "Applying changes…"
                    : window.backend.changeCount === 1 ? "1 change" : window.backend.changeCount + " changes"
                Layout.fillWidth: true
                elide: Label.ElideRight
            }
            Button {
                text: "Discard"
                flat: true
                enabled: !window.backend.applying
                onClicked: window.backend.discard()
            }
            Button {
                text: "Apply"
                highlighted: true
                enabled: !window.backend.applying
                onClicked: window.backend.apply()
            }
        }
    }

    // Marked changes are not dropped by closing the window by accident.
    onClosing: close => {
        if (backend.changeCount > 0 && !backend.applying && !leave.discarded) {
            close.accepted = false
            leave.open()
        }
    }

    Dialog {
        id: leave
        property bool discarded: false
        title: "Apply your changes?"
        modal: true
        anchors.centerIn: parent
        width: Math.min(window.width - window.gap * 2, 420)
        Label {
            anchors.fill: parent
            wrapMode: Label.Wrap
            text: window.backend.changeCount === 1
                ? "1 change is marked but not applied yet."
                : window.backend.changeCount + " changes are marked but not applied yet."
        }
        footer: DialogButtonBox {
            Button {
                text: "Apply"
                highlighted: true
                DialogButtonBox.buttonRole: DialogButtonBox.AcceptRole
            }
            Button {
                text: "Close without them"
                DialogButtonBox.buttonRole: DialogButtonBox.DestructiveRole
            }
            Button {
                text: "Cancel"
                DialogButtonBox.buttonRole: DialogButtonBox.RejectRole
            }
            onClicked: button => {
                if (button.DialogButtonBox.buttonRole === DialogButtonBox.DestructiveRole) {
                    leave.discarded = true
                    leave.close()
                    window.close()
                }
            }
        }
        onAccepted: window.backend.apply()
    }

    StackView {
        id: stack
        anchors.fill: parent
        initialItem: mainPage

        pushEnter: Transition {
            NumberAnimation { property: "x"; from: stack.width; to: 0; duration: 500; easing.type: Easing.BezierSpline; easing.bezierCurve: window.showCurve }
        }
        pushExit: Transition {
            NumberAnimation { property: "x"; from: 0; to: -stack.width / 4; duration: 500; easing.type: Easing.BezierSpline; easing.bezierCurve: window.showCurve }
        }
        popEnter: Transition {
            NumberAnimation { property: "x"; from: -stack.width / 4; to: 0; duration: 500; easing.type: Easing.BezierSpline; easing.bezierCurve: window.showCurve }
        }
        popExit: Transition {
            NumberAnimation { property: "x"; to: stack.width; duration: 500; easing.type: Easing.BezierSpline; easing.bezierCurve: window.showCurve }
        }

        // Swipe right anywhere goes back one page.
        DragHandler {
            target: null
            enabled: stack.depth > 1
            xAxis.enabled: true
            yAxis.enabled: false
            onActiveChanged: {
                if (!active && translation.x > 80) {
                    stack.pop()
                }
            }
        }
    }

    Shortcut {
        sequences: [StandardKey.Back, "Alt+Left", "Escape"]
        enabled: stack.depth > 1
        onActivated: stack.pop()
    }

    Component.onCompleted: openPage(startPage)

    Component {
        id: mainPage
        Page {
            title: "Settings"
            background: Rectangle { color: palette.window }
            ScrollView {
                anchors.fill: parent
                contentWidth: availableWidth
                ColumnLayout {
                    width: parent.width
                    spacing: window.gap / 2
                    Item { implicitHeight: window.gap / 2 }
                    SectionRow {
                        title: "gjallarHorn"
                        detail: "Security checks for this device and what to do about them."
                        iconName: "security-high"
                        onClicked: window.openPage("security")
                    }
                    SectionRow {
                        title: "Apps"
                        detail: "Install and remove apps from the gjallarOS catalogue."
                        iconName: "system-software-install"
                        onClicked: window.openPage("apps")
                    }
                }
            }
        }
    }

    component SectionRow: ItemDelegate {
        id: row
        required property string title
        required property string detail
        property string iconName: ""
        Layout.fillWidth: true
        Layout.leftMargin: window.gap
        Layout.rightMargin: window.gap
        padding: window.gap
        background: Rectangle {
            radius: 6
            color: row.hovered ? Qt.tint(palette.alternateBase, Qt.rgba(palette.highlight.r, palette.highlight.g, palette.highlight.b, 0.12)) : palette.alternateBase
        }
        contentItem: RowLayout {
            spacing: window.gap
            IconImage {
                name: row.iconName
                visible: row.iconName !== ""
                sourceSize.width: 28
                sourceSize.height: 28
            }
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 2
                Label { text: row.title; textFormat: Text.PlainText; font.pixelSize: 16; font.weight: Font.DemiBold }
                Label { text: row.detail; textFormat: Text.PlainText; color: palette.placeholderText; wrapMode: Label.Wrap; Layout.fillWidth: true }
            }
            Label { text: "›"; font.pixelSize: 22; color: palette.placeholderText }
        }
    }

    Component {
        id: securityPage
        SecurityPage { backend: window.backend; colours: window.colours; gap: window.gap }
    }

    Component {
        id: appsPage
        AppsPage { backend: window.backend; colours: window.colours; gap: window.gap; installer: window.wineInstaller }
    }
}

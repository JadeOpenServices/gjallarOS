import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// gjallarHorn (Plane 216): every check of `gjallarctl security status`.
// A protection the user turned off is grey and says so; it never warns.
Page {
    id: page

    required property var backend
    required property var colours
    required property int gap
    title: "gjallarHorn"

    readonly property int open: {
        let n = 0
        for (const c of backend.checks) {
            if (c.state === "weak" || c.state === "bad") {
                n++
            }
        }
        return n
    }

    function stateColour(state) {
        switch (state) {
        case "ok": return colours.positive
        case "weak": return colours.neutral
        case "bad": return colours.negative
        default: return palette.placeholderText
        }
    }

    function stateText(state) {
        switch (state) {
        case "ok": return "OK"
        case "weak": return "Look at this"
        case "bad": return "Problem"
        case "off": return "Off (your choice)"
        default: return "Not confirmed"
        }
    }

    background: Rectangle { color: palette.window }
    Component.onCompleted: backend.refreshSecurity()

    ScrollView {
        anchors.fill: parent
        contentWidth: availableWidth

        ColumnLayout {
            width: parent.width
            spacing: page.gap / 2

            Label {
                Layout.fillWidth: true
                Layout.margins: page.gap
                wrapMode: Label.Wrap
                font.pixelSize: 15
                text: page.backend.checks.length === 0
                    ? (page.backend.busy ? "Checking this device…" : "The checks could not run.")
                    : page.open === 0 ? "Nothing needs your attention."
                    : page.open === 1 ? "1 thing needs your attention."
                    : page.open + " things need your attention."
            }

            Repeater {
                model: page.backend.checks

                delegate: Pane {
                    id: row
                    required property var modelData
                    Layout.fillWidth: true
                    Layout.leftMargin: page.gap
                    Layout.rightMargin: page.gap
                    padding: page.gap
                    background: Rectangle { radius: 12; color: palette.alternateBase }

                    RowLayout {
                        anchors.fill: parent
                        spacing: page.gap

                        Rectangle {
                            Layout.alignment: Qt.AlignTop
                            Layout.topMargin: 6
                            implicitWidth: 10
                            implicitHeight: 10
                            radius: 5
                            color: page.stateColour(row.modelData.state)
                        }

                        ColumnLayout {
                            Layout.fillWidth: true
                            spacing: 2
                            RowLayout {
                                Layout.fillWidth: true
                                Label {
                                    text: row.modelData.title
                                    font.pixelSize: 15
                                    font.weight: Font.DemiBold
                                    color: row.modelData.state === "off" ? palette.placeholderText : palette.text
                                    Layout.fillWidth: true
                                    elide: Label.ElideRight
                                }
                                Label {
                                    text: page.stateText(row.modelData.state)
                                    color: page.stateColour(row.modelData.state)
                                }
                            }
                            Label {
                                text: row.modelData.detail
                                color: palette.placeholderText
                                wrapMode: Label.Wrap
                                Layout.fillWidth: true
                            }
                            RowLayout {
                                visible: fixButton.visible || protectionSwitch.visible
                                Layout.fillWidth: true
                                Layout.topMargin: page.gap / 2
                                Button {
                                    id: fixButton
                                    visible: (row.modelData.fix || "") !== ""
                                    text: row.modelData.fix || ""
                                    highlighted: true
                                    onClicked: page.backend.fix(row.modelData.id)
                                }
                                Item { Layout.fillWidth: true }
                                Switch {
                                    id: protectionSwitch
                                    visible: (row.modelData.switch || "") !== ""
                                    text: checked ? "On" : "Off"
                                    checked: row.modelData.switch === "on"
                                    // The settings service asks for the password;
                                    // the switch follows once the change is in.
                                    onToggled: {
                                        page.backend.switchProtection(row.modelData.id, checked)
                                        checked = Qt.binding(() => row.modelData.switch === "on")
                                    }
                                }
                            }
                        }
                    }
                }
            }

            Button {
                Layout.alignment: Qt.AlignHCenter
                Layout.margins: page.gap
                text: "Check again"
                flat: true
                enabled: !page.backend.busy
                onClicked: page.backend.refreshSecurity()
            }
        }
    }
}

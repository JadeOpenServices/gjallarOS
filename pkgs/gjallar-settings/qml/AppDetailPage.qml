import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// One app in full (Plane 276): what it is, what it can reach, its licence
// and homepage, and the same marks as its row. Catalogue apps and Wine
// apps share the page; a Wine app also picks its program and can be
// removed here. Everything shown is built in or read from gjallarctl:
// the page fetches nothing.
Page {
    id: page

    required property var backend
    required property var colours
    required property int gap
    required property string appId
    required property bool wine

    // Follows the lists, so a mark or a refresh shows here at once.
    readonly property var entry: (wine ? backend.wineApps : backend.apps).find(e => e.id === appId) || null
    // A Wine app's licence and homepage are Wine's own.
    readonly property var details: backend.appDetails[wine ? "wine" : appId] || ({})
    readonly property string name: entry ? (entry.name || entry.id) : ""
    readonly property string licences: (details.licences || []).join(", ")
    title: name

    background: Rectangle { color: palette.window }

    // A Wine app removed here leaves the page.
    onEntryChanged: {
        if (entry === null && StackView.status === StackView.Active) {
            StackView.view.pop()
        }
    }

    Dialog {
        id: remove
        title: "Remove " + page.name + "?"
        modal: true
        anchors.centerIn: parent
        width: Math.min(page.width - page.gap * 2, 420)
        onOpened: deleteFiles.checked = false

        ColumnLayout {
            anchors.fill: parent
            spacing: page.gap / 2
            Label {
                textFormat: Text.PlainText
                text: "Its sandbox, menu entry and network choices go away."
                wrapMode: Label.Wrap
                Layout.fillWidth: true
            }
            CheckBox {
                id: deleteFiles
                text: "Also delete its files in WineApps/" + page.name
                Layout.fillWidth: true
            }
        }

        footer: DialogButtonBox {
            Button {
                text: "Remove"
                DialogButtonBox.buttonRole: DialogButtonBox.AcceptRole
            }
            Button {
                text: "Cancel"
                DialogButtonBox.buttonRole: DialogButtonBox.RejectRole
            }
        }
        onAccepted: page.backend.removeWine(page.appId, deleteFiles.checked)
    }

    ScrollView {
        anchors.fill: parent
        contentWidth: availableWidth
        visible: page.entry !== null

        ColumnLayout {
            width: parent.width
            spacing: page.gap

            // Header: icon, name, summary, state and the one button.
            RowLayout {
                Layout.fillWidth: true
                Layout.margins: page.gap
                Layout.bottomMargin: 0
                spacing: page.gap
                Image {
                    source: "image://appicon/" + (page.wine ? "wine" : page.appId)
                    sourceSize: Qt.size(64, 64)
                    Layout.preferredWidth: 64
                    Layout.preferredHeight: 64
                    Layout.alignment: Qt.AlignTop
                    Accessible.ignored: true
                }
                ColumnLayout {
                    Layout.fillWidth: true
                    spacing: 2
                    Label {
                        textFormat: Text.PlainText
                        text: page.name
                        font.pixelSize: 20
                        font.weight: Font.DemiBold
                        wrapMode: Label.Wrap
                        Layout.fillWidth: true
                    }
                    Label {
                        textFormat: Text.PlainText
                        text: !page.entry ? ""
                            : !page.wine ? page.entry.description
                            : page.entry.removed ? "Removed"
                            : page.entry.error ? page.entry.error
                            : page.entry.program || "No program picked"
                        color: page.wine && page.entry && page.entry.error ? page.colours.neutral : palette.placeholderText
                        wrapMode: page.wine ? Label.WrapAnywhere : Label.Wrap
                        Layout.fillWidth: true
                    }
                    Label {
                        readonly property bool known: !page.wine && page.entry !== null && typeof page.entry.installed === "boolean"
                        readonly property bool waits: known && typeof page.entry.built === "boolean"
                            && page.entry.installed !== page.entry.built
                        visible: known
                        textFormat: Text.PlainText
                        text: !known ? ""
                            : appButton.marked ? (page.entry.installed ? "Will be removed" : "Will be installed")
                            : waits ? (page.entry.installed ? "Installed with the next rebuild" : "Removed with the next rebuild")
                            : page.entry.installed ? "Installed" : "Not installed"
                        color: appButton.marked ? palette.highlight : waits ? page.colours.neutral : palette.placeholderText
                        font.weight: appButton.marked ? Font.DemiBold : Font.Normal
                        wrapMode: Label.Wrap
                        Layout.fillWidth: true
                    }
                }
                MarkButton {
                    id: appButton
                    visible: !page.wine
                    backend: page.backend
                    kind: "app"
                    itemId: page.wine ? "" : page.appId
                    value: !page.wine && page.entry !== null && page.entry.installed === true
                    allowed: !page.wine && page.entry !== null && typeof page.entry.installed === "boolean"
                    onText: "Install"
                    offText: "Remove"
                    label: page.name
                    Layout.alignment: Qt.AlignTop
                }
            }

            Section {
                heading: "What it can reach"
                Label {
                    textFormat: Text.PlainText
                    text: page.wine
                        ? "Runs in its own sandbox. It sees only its folder WineApps/" + page.name
                            + ", not the rest of your files, and only the networks turned on below."
                        : "Runs as you: it can read and change your files and use the network."
                    wrapMode: Label.Wrap
                    Layout.fillWidth: true
                }
                Label {
                    visible: !page.wine && page.details.system === true
                    textFormat: Text.PlainText
                    text: "It also sets up parts of the system outside your account, such as background services or locked settings."
                    wrapMode: Label.Wrap
                    Layout.fillWidth: true
                }
                Repeater {
                    model: page.wine ? [
                        { key: "internet", label: "Internet", on: "On" },
                        { key: "home", label: "Home network", on: "On · home + tailnet" },
                    ] : []
                    RowLayout {
                        id: net
                        required property var modelData
                        readonly property bool value: page.entry !== null && page.entry[modelData.key] === true
                        spacing: page.gap
                        Layout.fillWidth: true
                        Label {
                            textFormat: Text.PlainText
                            text: net.modelData.label
                            Layout.fillWidth: true
                        }
                        Label {
                            textFormat: Text.PlainText
                            text: netButton.marked ? (net.value ? "Will turn off" : "Will turn on")
                                : net.value ? net.modelData.on : "Off"
                            color: netButton.marked ? palette.highlight : palette.placeholderText
                            font.weight: netButton.marked ? Font.DemiBold : Font.Normal
                        }
                        MarkButton {
                            id: netButton
                            backend: page.backend
                            kind: net.modelData.key
                            itemId: page.appId
                            value: net.value
                            // A removed app's networks can only go off.
                            allowed: page.backend.wineSettings === "ok" && page.entry !== null
                                && (!page.entry.removed || net.value)
                            label: net.modelData.label + " for " + page.name
                        }
                    }
                }
            }

            Section {
                visible: page.wine && page.backend.wineTool && page.entry !== null && !page.entry.removed && !page.entry.error
                heading: "Program"
                Label {
                    textFormat: Text.PlainText
                    text: page.entry && page.entry.program
                        ? "Starts " + page.entry.program + "."
                        : "No program picked yet. Pick one of the programs its shortcuts start."
                    wrapMode: Label.WrapAnywhere
                    Layout.fillWidth: true
                }
                Button {
                    text: page.entry && page.entry.program ? "Change program…" : "Pick program…"
                    enabled: !page.backend.busy
                    Accessible.name: "Pick the program " + page.name + " starts"
                    onClicked: page.backend.pickWineProgram(page.appId)
                }
            }

            Section {
                heading: "Licence"
                Label {
                    textFormat: Text.PlainText
                    text: page.wine
                        ? "Runs with Wine" + (page.licences ? " (" + page.licences + ")" : "")
                            + ". The app's own licence comes with its installer."
                        : page.licences || "Not listed in the catalogue."
                    wrapMode: Label.Wrap
                    Layout.fillWidth: true
                }
            }

            Section {
                visible: (page.details.homepage || "") !== ""
                heading: page.wine ? "Wine homepage" : "Homepage"
                RowLayout {
                    spacing: page.gap
                    Layout.fillWidth: true
                    Label {
                        textFormat: Text.PlainText
                        text: page.details.homepage || ""
                        color: palette.placeholderText
                        elide: Label.ElideMiddle
                        Layout.fillWidth: true
                    }
                    Button {
                        text: "Open"
                        Accessible.name: "Open the homepage in the browser"
                        onClicked: page.backend.openHomepage(page.wine ? "wine" : page.appId)
                    }
                }
            }

            Section {
                visible: page.wine && page.backend.wineTool
                heading: "Remove"
                Label {
                    textFormat: Text.PlainText
                    text: "Removes the app from this device. Its files can stay in WineApps/" + page.name + "."
                    wrapMode: Label.Wrap
                    Layout.fillWidth: true
                }
                Button {
                    text: "Remove…"
                    enabled: !page.backend.busy && !page.backend.applying
                    Accessible.name: "Remove " + page.name
                    onClicked: remove.open()
                }
            }

            Item { implicitHeight: page.gap }
        }
    }

    // A heading and a card, like the rows on the list.
    component Section: ColumnLayout {
        id: section
        required property string heading
        default property alias content: card.data
        Layout.fillWidth: true
        Layout.leftMargin: page.gap
        Layout.rightMargin: page.gap
        spacing: page.gap / 4
        Label {
            textFormat: Text.PlainText
            text: section.heading
            font.weight: Font.DemiBold
            color: palette.highlight
            leftPadding: page.gap
        }
        Pane {
            Layout.fillWidth: true
            padding: page.gap
            background: Rectangle { radius: 12; color: palette.alternateBase }
            contentItem: ColumnLayout {
                id: card
                spacing: page.gap / 2
            }
        }
    }
}

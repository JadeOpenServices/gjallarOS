import QtQuick
import QtQuick.Controls
import QtQuick.Dialogs
import QtQuick.Layouts

// The app catalogue (Plane 220): `gjallarctl apps list --json`, one switch
// per app. Installing or removing runs in a terminal with the password
// prompt and the build output. Wine apps (Plane 272) follow in their own
// section with their two network switches, and new ones are set up from
// the end of the list.
Page {
    id: page

    required property var backend
    required property var colours
    required property int gap
    title: "Apps"

    // Sorted by category so the list sections stay together.
    readonly property var shown: {
        const query = search.text.trim().toLowerCase()
        const list = backend.apps.filter(a => query === ""
            || a.name.toLowerCase().includes(query)
            || a.description.toLowerCase().includes(query)
            || a.id.toLowerCase().includes(query))
        list.sort((a, b) => a.category.localeCompare(b.category) || a.name.localeCompare(b.name))
        const wine = backend.wineApps.filter(w => query === ""
            || (w.name || "").toLowerCase().includes(query)
            || (w.program || "").toLowerCase().includes(query)
            || w.id.toLowerCase().includes(query))
        return list.concat(wine.map(w => Object.assign({ wine: true, category: "Wine apps" }, w)))
    }
    // installed is null (undefined here) when the settings service does not answer.
    readonly property bool known: backend.apps.length > 0 && backend.apps.every(a => typeof a.installed === "boolean")

    background: Rectangle { color: palette.window }
    Component.onCompleted: {
        backend.refreshApps()
        backend.refreshWine()
    }

    FileDialog {
        id: installerDialog
        title: "Installer"
        nameFilters: ["Installers (*.exe *.msi *.EXE *.MSI)"]
        currentFolder: "file://" + encodeURI(page.backend.downloads)
        onAccepted: setUp.installer = selectedFile.toString()
    }

    Dialog {
        id: setUp
        property string installer: ""
        readonly property string installerName: decodeURIComponent(installer.slice(installer.lastIndexOf("/") + 1))
        title: "New Wine app"
        modal: true
        anchors.centerIn: parent
        width: Math.min(page.width - page.gap * 2, 420)
        onOpened: {
            installer = ""
            appName.text = ""
            appName.forceActiveFocus()
        }

        ColumnLayout {
            anchors.fill: parent
            spacing: page.gap / 2
            Label { text: "Name" }
            TextField {
                id: appName
                Layout.fillWidth: true
                placeholderText: "Example Editor"
                Accessible.name: "Name"
                maximumLength: 64
            }
            Label {
                text: "Its files go in WineApps/" + (appName.text.trim() || "…")
                color: palette.placeholderText
                wrapMode: Label.WrapAnywhere
                Layout.fillWidth: true
            }
            RowLayout {
                spacing: page.gap / 2
                Layout.fillWidth: true
                Button {
                    text: "Choose installer…"
                    onClicked: installerDialog.open()
                }
                Label {
                    text: setUp.installerName || "None chosen"
                    color: palette.placeholderText
                    elide: Label.ElideMiddle
                    Layout.fillWidth: true
                }
            }
        }

        footer: DialogButtonBox {
            Button {
                text: "Set up"
                enabled: appName.text.trim() !== "" && !appName.text.trim().startsWith(".")
                    && !appName.text.includes("/") && setUp.installer !== ""
                DialogButtonBox.buttonRole: DialogButtonBox.AcceptRole
            }
            Button {
                text: "Cancel"
                DialogButtonBox.buttonRole: DialogButtonBox.RejectRole
            }
        }
        onAccepted: page.backend.setUpWine(appName.text, installer)
    }

    ColumnLayout {
        anchors.fill: parent
        spacing: page.gap / 2

        TextField {
            id: search
            Layout.fillWidth: true
            Layout.margins: page.gap
            Layout.bottomMargin: 0
            placeholderText: "Search apps"
            Accessible.name: "Search apps"
            onTextChanged: list.positionViewAtBeginning()
        }

        Label {
            readonly property bool wineUnknown: page.backend.wineApps.length > 0 && page.backend.wineSettings !== "ok"
            visible: (page.backend.apps.length > 0 && !page.known) || wineUnknown
            Layout.fillWidth: true
            Layout.leftMargin: page.gap
            Layout.rightMargin: page.gap
            wrapMode: Label.Wrap
            color: page.colours.neutral
            text: (page.known ? page.backend.wineSettings : page.backend.appsSettings) === "missing"
                ? "This system still builds from your own checkout, so this list cannot show what is installed or change it. Run gjallarctl system move to switch to the system checkout."
                : "The settings service did not answer, so this list cannot show what is installed or change it."
        }

        Label {
            visible: page.backend.apps.length === 0
            Layout.fillWidth: true
            Layout.margins: page.gap
            wrapMode: Label.Wrap
            text: page.backend.busy ? "Reading the catalogue…" : "The catalogue could not be read."
        }

        ListView {
            id: list
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            spacing: page.gap / 2
            // A count, not the list itself: a new list would rebuild every row
            // and scroll back to the top each time a switch changes. The rows
            // read their entry from page.shown and follow it in place.
            model: page.shown.length
            boundsBehavior: Flickable.StopAtBounds
            leftMargin: page.gap
            rightMargin: page.gap
            ScrollBar.vertical: ScrollBar {}

            footer: Item {
                visible: page.backend.wineTool
                width: ListView.view.width - page.gap * 2
                height: visible ? newWine.implicitHeight + page.gap * 2 : 0
                Button {
                    id: newWine
                    anchors.verticalCenter: parent.verticalCenter
                    text: "Set up a Wine app…"
                    enabled: !page.backend.busy
                    onClicked: setUp.open()
                }
            }

            delegate: ColumnLayout {
                id: row
                required property int index
                readonly property var entry: page.shown[row.index] || {}
                width: ListView.view.width - page.gap * 2
                spacing: 0

                // Section headings live in the rows: ListView sections need
                // a model with roles.
                Label {
                    readonly property string category: row.entry.category || ""
                    visible: row.index === 0 || (page.shown[row.index - 1] || {}).category !== category
                    Layout.fillWidth: true
                    leftPadding: page.gap
                    topPadding: page.gap
                    bottomPadding: page.gap / 4
                    text: category.charAt(0).toUpperCase() + category.slice(1)
                    font.weight: Font.DemiBold
                    color: palette.highlight
                }

                Pane {
                    Layout.fillWidth: true
                    padding: page.gap
                    background: Rectangle { radius: 12; color: palette.alternateBase }
                    contentItem: Loader {
                        sourceComponent: row.entry.wine ? wineRow : appRow
                    }

                    Component {
                        id: appRow
                        RowLayout {
                            spacing: page.gap
                            Image {
                                source: "image://appicon/" + row.entry.id
                                sourceSize: Qt.size(40, 40)
                                Layout.preferredWidth: 40
                                Layout.preferredHeight: 40
                                Layout.alignment: Qt.AlignTop
                                Accessible.ignored: true
                            }
                            ColumnLayout {
                                Layout.fillWidth: true
                                spacing: 2
                                Label {
                                    text: row.entry.name
                                    font.pixelSize: 15
                                    font.weight: Font.DemiBold
                                    Layout.fillWidth: true
                                    elide: Label.ElideRight
                                }
                                Label {
                                    text: row.entry.description
                                    color: palette.placeholderText
                                    wrapMode: Label.Wrap
                                    Layout.fillWidth: true
                                }
                                // The saved choice differs from the running system.
                                Label {
                                    readonly property bool waits: typeof row.entry.installed === "boolean"
                                        && typeof row.entry.built === "boolean"
                                        && row.entry.installed !== row.entry.built
                                    visible: waits
                                    text: row.entry.installed
                                        ? (page.backend.appsRebuilding ? "Installing now" : "Installed with the next rebuild")
                                        : (page.backend.appsRebuilding ? "Removing now" : "Removed with the next rebuild")
                                    color: page.colours.neutral
                                    wrapMode: Label.Wrap
                                    Layout.fillWidth: true
                                }
                            }
                            Switch {
                                enabled: typeof row.entry.installed === "boolean" && !page.backend.busy
                                checked: row.entry.installed === true
                                Accessible.name: (checked ? "Remove " : "Install ") + row.entry.name
                                // The switch follows the catalogue once the change is in.
                                onToggled: {
                                    page.backend.setApp(row.entry.id, checked)
                                    checked = Qt.binding(() => row.entry.installed === true)
                                }
                            }
                        }
                    }

                    Component {
                        id: wineRow
                        ColumnLayout {
                            spacing: 2
                            Label {
                                text: row.entry.name || row.entry.id
                                font.pixelSize: 15
                                font.weight: Font.DemiBold
                                Layout.fillWidth: true
                                elide: Label.ElideRight
                            }
                            Label {
                                text: row.entry.removed ? "Removed"
                                    : row.entry.error ? row.entry.error
                                    : row.entry.program || "No program picked"
                                color: row.entry.error ? page.colours.neutral : palette.placeholderText
                                wrapMode: Label.WrapAnywhere
                                Layout.fillWidth: true
                            }
                            Button {
                                visible: page.backend.wineTool && !row.entry.removed && !row.entry.error
                                    && !row.entry.program
                                enabled: !page.backend.busy
                                text: "Pick program…"
                                Accessible.name: "Pick the program " + (row.entry.name || row.entry.id) + " starts"
                                onClicked: page.backend.pickWineProgram(row.entry.id)
                            }
                            Repeater {
                                model: [
                                    { key: "internet", label: "Internet", on: "On" },
                                    { key: "home", label: "Home network", on: "On · home + tailnet" },
                                ]
                                RowLayout {
                                    required property var modelData
                                    readonly property bool value: row.entry[modelData.key] === true
                                    spacing: page.gap
                                    Layout.fillWidth: true
                                    Label {
                                        text: parent.modelData.label
                                        Layout.fillWidth: true
                                    }
                                    Label {
                                        text: parent.value ? parent.modelData.on : "Off"
                                        color: palette.placeholderText
                                    }
                                    Switch {
                                        id: netSwitch
                                        // A removed app's switches can only go off.
                                        enabled: page.backend.wineSettings === "ok" && !page.backend.busy
                                            && (!row.entry.removed || checked)
                                        checked: parent.value
                                        Accessible.name: parent.modelData.label + " for " + (row.entry.name || row.entry.id)
                                        onToggled: {
                                            page.backend.setWine(row.entry.id, parent.modelData.key, checked)
                                            checked = Qt.binding(() => parent.value)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// The app catalogue (Plane 220): `gjallarctl apps list --json`, one switch
// per app. Installing or removing runs in a terminal with the password
// prompt and the build output.
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
        return list.sort((a, b) => a.category.localeCompare(b.category) || a.name.localeCompare(b.name))
    }
    // installed is null (undefined here) when the settings service does not answer.
    readonly property bool known: backend.apps.length > 0 && backend.apps.every(a => typeof a.installed === "boolean")

    background: Rectangle { color: palette.window }
    Component.onCompleted: backend.refreshApps()

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
        }

        Label {
            visible: page.backend.apps.length > 0 && !page.known
            Layout.fillWidth: true
            Layout.leftMargin: page.gap
            Layout.rightMargin: page.gap
            wrapMode: Label.Wrap
            color: page.colours.neutral
            text: page.backend.appsSettings === "missing"
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
            model: page.shown
            boundsBehavior: Flickable.StopAtBounds
            leftMargin: page.gap
            rightMargin: page.gap
            ScrollBar.vertical: ScrollBar {}

            section.property: "category"
            section.delegate: Label {
                required property string section
                width: ListView.view.width - page.gap * 2
                leftPadding: page.gap
                topPadding: page.gap
                bottomPadding: page.gap / 4
                text: section.charAt(0).toUpperCase() + section.slice(1)
                font.weight: Font.DemiBold
                color: palette.highlight
            }

            delegate: Pane {
                id: row
                required property var modelData
                width: ListView.view.width - page.gap * 2
                padding: page.gap
                background: Rectangle { radius: 12; color: palette.alternateBase }

                RowLayout {
                    anchors.fill: parent
                    spacing: page.gap
                    Image {
                        source: "image://appicon/" + row.modelData.id
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
                            text: row.modelData.name
                            font.pixelSize: 15
                            font.weight: Font.DemiBold
                            Layout.fillWidth: true
                            elide: Label.ElideRight
                        }
                        Label {
                            text: row.modelData.description
                            color: palette.placeholderText
                            wrapMode: Label.Wrap
                            Layout.fillWidth: true
                        }
                        // The saved choice differs from the running system.
                        Label {
                            readonly property bool waits: typeof row.modelData.installed === "boolean"
                                && typeof row.modelData.built === "boolean"
                                && row.modelData.installed !== row.modelData.built
                            visible: waits
                            text: row.modelData.installed
                                ? (page.backend.appsRebuilding ? "Installing now" : "Installed with the next rebuild")
                                : (page.backend.appsRebuilding ? "Removing now" : "Removed with the next rebuild")
                            color: page.colours.neutral
                            wrapMode: Label.Wrap
                            Layout.fillWidth: true
                        }
                    }
                    Switch {
                        enabled: typeof row.modelData.installed === "boolean" && !page.backend.busy
                        checked: row.modelData.installed === true
                        Accessible.name: (checked ? "Remove " : "Install ") + row.modelData.name
                        // The switch follows the catalogue once the change is in.
                        onToggled: {
                            page.backend.setApp(row.modelData.id, checked)
                            checked = Qt.binding(() => row.modelData.installed === true)
                        }
                    }
                }
            }
        }
    }
}

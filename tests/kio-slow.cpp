#include <QCoreApplication>
#include <QUrl>
#include <KFileItem>
#include <KIO/StatJob>
#include <cstring>
#include <iostream>
// Prints "slow" or "fast" and the file type, as KIO sees a local path.
// With --stat, prints the type the file worker sends for a stat that asks
// for it, as Dolphin's preview and directory jobs do.
int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    if (argc == 3 && std::strcmp(argv[1], "--stat") == 0) {
        auto *job = KIO::stat(QUrl::fromLocalFile(QString::fromLocal8Bit(argv[2])),
                              KIO::StatJob::SourceSide,
                              KIO::StatBasic | KIO::StatMimeType,
                              KIO::HideProgressInfo);
        if (!job->exec()) return 1;
        std::cout << "stat " << job->statResult().stringValue(KIO::UDSEntry::UDS_MIME_TYPE).toStdString() << '\n';
        return 0;
    }
    if (argc != 2) return 2;
    KFileItem item(QUrl::fromLocalFile(QString::fromLocal8Bit(argv[1])));
    std::cout << (item.isSlow() ? "slow" : "fast") << ' ' << item.mimetype().toStdString() << '\n';
    return 0;
}

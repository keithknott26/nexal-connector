// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "NexalMac",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "NexalMac", targets: ["NexalMac"])],
    targets: [
        .executableTarget(name: "NexalMac"),
        .testTarget(name: "NexalMacTests", dependencies: ["NexalMac"])
    ]
)

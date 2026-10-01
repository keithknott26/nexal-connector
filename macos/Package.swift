// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "NexalMac",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "NexalMac", targets: ["NexalMac"]),
        .executable(name: "nexal-vmhost", targets: ["nexal-vmhost"])
    ],
    targets: [
        .executableTarget(name: "NexalMac"),
        .testTarget(name: "NexalMacTests", dependencies: ["NexalMac"]),
        .executableTarget(name: "nexal-vmhost", path: "Sources/NexalVMHost"),
        .testTarget(name: "NexalVMHostTests", dependencies: ["nexal-vmhost"])
    ]
)

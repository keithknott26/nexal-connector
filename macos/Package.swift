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
        // C half of the LAN bridge client: SCM_RIGHTS needs the CMSG_* macros.
        .target(name: "NexalVMNetClient", path: "Sources/NexalVMNetClient"),
        // Non-source files living beside the target. SwiftPM warns about anything it
        // cannot classify, and the wheel is a stray download, not a resource.
        .executableTarget(name: "nexal-vmhost", dependencies: ["NexalVMNetClient"], path: "Sources/NexalVMHost",
                          exclude: ["README.md", "SMOKE-TEST.md",
                                    "tree_sitter-0.26.0-cp310-cp310-manylinux2014_aarch64.manylinux_2_17_aarch64.manylinux_2_28_aarch64.whl"]),
        .testTarget(name: "NexalVMHostTests", dependencies: ["nexal-vmhost"])
    ]
)

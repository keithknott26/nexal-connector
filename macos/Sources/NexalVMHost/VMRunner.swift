import Foundation
import Virtualization

/// Thread-safe phase shared with the control socket thread.
final class RunState: @unchecked Sendable {
    private let lock = NSLock()
    private var value = "starting"
    var phase: String {
        get { lock.lock(); defer { lock.unlock() }; return value }
        set { lock.lock(); value = newValue; lock.unlock() }
    }
}

/// Owns the running VM. Everything touching Virtualization.framework objects
/// happens on the main actor. Deliberately takes no power assertion: the guest
/// is suspended with the Mac. If a vsock/console agent is enabled, the guest
/// should re-sync its clock when the host wakes (see README).
@MainActor
final class VMRunner: NSObject, VZVirtualMachineDelegate {
    private static var retained: VMRunner?

    private let spec: VMSpec
    private let vm: VZVirtualMachine
    private let state = RunState()
    private var control: ControlServer?
    private var bridge: GuestBridge?
    private var signalSources: [DispatchSourceSignal] = []
    private var stopRequested = false
    private var stopIssued = false
    private var finished = false
    private let bridgeFD: Int32?

    init(spec: VMSpec, built: BuiltVM) {
        self.spec = spec
        self.vm = VZVirtualMachine(configuration: built.configuration)
        self.bridgeFD = built.guestBridgeFD
        super.init()
        vm.delegate = self
    }

    func start() throws {
        VMRunner.retained = self

        let state = self.state
        let server = ControlServer(path: spec.controlSocket) { [weak self] cmd in
            switch cmd {
            case "status":
                return state.phase
            case "stop":
                DispatchQueue.main.async { Task { @MainActor in self?.requestStop() } }
                return "ok"
            default:
                return "error: unknown command"
            }
        }
        try server.start()
        control = server

        if let fd = bridgeFD, let path = spec.guest {
            let b = GuestBridge(path: path, vmEndFD: fd)
            try b.start()
            bridge = b
        }

        for sig in [SIGTERM, SIGINT] {
            signal(sig, SIG_IGN)
            let src = DispatchSource.makeSignalSource(signal: sig, queue: .main)
            src.setEventHandler { [weak self] in Task { @MainActor in self?.requestStop() } }
            src.resume()
            signalSources.append(src)
        }

        Task { @MainActor in
            do {
                try await self.vm.start()
                self.state.phase = "running"
                if self.stopRequested { self.requestStop() }
            } catch {
                self.finish(code: 1, message: "VM failed to start: \(error.localizedDescription)")
            }
        }
    }

    /// ACPI power button, then a forced stop after the grace period.
    func requestStop() {
        guard !finished else { return }
        stopRequested = true
        // Still booting: start()'s completion calls us again once the VM runs.
        guard state.phase != "starting" else { return }
        guard !stopIssued else { return }
        stopIssued = true
        state.phase = "stopping"
        do {
            guard vm.canRequestStop else { throw SpecError("guest cannot accept a power button request") }
            try vm.requestStop()
        } catch {
            Task { @MainActor in await self.forceStop() }
            return
        }
        let grace = spec.graceSeconds
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: UInt64(grace) * 1_000_000_000)
            await self.forceStop()
        }
    }

    private func forceStop() async {
        guard !finished else { return }
        do {
            try await vm.stop()
            finish(code: 0, message: nil)
        } catch {
            finish(code: 1, message: "forced stop failed: \(error.localizedDescription)")
        }
    }

    private func finish(code: Int32, message: String?) {
        guard !finished else { return }
        finished = true
        state.phase = "stopped"
        control?.stop()
        bridge?.stop()
        if let message { FileHandle.standardError.write(Data(("nexal-vmhost: " + message + "\n").utf8)) }
        exit(code)
    }

    // MARK: VZVirtualMachineDelegate

    nonisolated func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        Task { @MainActor in self.finish(code: 0, message: nil) }
    }

    nonisolated func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        let msg = "VM stopped with error: \(error.localizedDescription)"
        Task { @MainActor in self.finish(code: 1, message: msg) }
    }
}

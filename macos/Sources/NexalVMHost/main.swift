import Foundation

let version = "0.1.0"

func fail(_ message: String, code: Int32 = 1) -> Never {
    FileHandle.standardError.write(Data(("nexal-vmhost: " + message + "\n").utf8))
    exit(code)
}

func option(_ name: String, in args: [String]) -> String? {
    guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
    return args[i + 1]
}

let args = Array(CommandLine.arguments.dropFirst())

switch args.first {
case "--version", "version":
    print("nexal-vmhost \(version)")

case "stop":
    guard let sock = option("--control", in: args) else { fail("usage: nexal-vmhost stop --control <socket>", code: 2) }
    do {
        let reply = try ControlClient.send(path: sock, command: "stop")
        if reply != "ok" { fail("stop refused: \(reply)") }
    } catch {
        fail("\(error)")
    }

case "run":
    guard let specPath = option("--spec", in: args) else { fail("usage: nexal-vmhost run --spec <file>", code: 2) }
    do {
        let spec = try VMSpec.load(path: specPath)
        try SpecValidator.validate(spec)
        // Refuse to start twice for the same control socket.
        if (try? ControlClient.send(path: spec.controlSocket, command: "status")) != nil {
            fail("a VM host is already running on \(spec.controlSocket)")
        }
        MainActor.assumeIsolated {
            do {
                let built = try VMConfigBuilder.build(spec)
                try VMRunner(spec: spec, built: built).start()
            } catch {
                fail("\(error)")
            }
        }
    } catch {
        fail("\(error)")
    }
    dispatchMain()

default:
    fail("usage: nexal-vmhost run --spec <file> | stop --control <socket> | --version", code: 2)
}

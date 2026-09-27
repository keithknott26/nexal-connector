import Foundation
import AppKit

/// The neXal pairing chime: two rising bell-like notes, about half a second.
///
/// Synthesized rather than shipped as an audio file so the iPhone app and the
/// Mac connector play exactly the same sound from the same few lines, and so the
/// tone is ours rather than a system sound the owner already hears for mail or
/// messages. 16-bit mono PCM WAV, built once.
enum PairingChimeSound {
    static let wav: Data = {
        let rate = 44_100
        let notes: [(frequency: Double, start: Double, length: Double)] = [
            (1318.51, 0.00, 0.40),   // E6
            (1975.53, 0.13, 0.55),   // B6
        ]
        let duration = 0.70
        var mix = [Double](repeating: 0, count: Int(duration * Double(rate)))
        for note in notes {
            let first = Int(note.start * Double(rate))
            for i in 0..<Int(note.length * Double(rate)) where first + i < mix.count {
                let t = Double(i) / Double(rate)
                // 4 ms attack, exponential decay: a struck bell, not a beep.
                let envelope = min(1, t / 0.004) * exp(-t * 7)
                let tone = sin(2 * .pi * note.frequency * t) + 0.25 * sin(4 * .pi * note.frequency * t)
                mix[first + i] += tone * envelope * 0.32
            }
        }
        var data = Data()
        func append(_ text: String) { data.append(contentsOf: Array(text.utf8)) }
        func append32(_ value: UInt32) { withUnsafeBytes(of: value.littleEndian) { data.append(contentsOf: $0) } }
        func append16(_ value: UInt16) { withUnsafeBytes(of: value.littleEndian) { data.append(contentsOf: $0) } }
        let payload = UInt32(mix.count * 2)
        append("RIFF"); append32(36 + payload); append("WAVE")
        append("fmt "); append32(16); append16(1); append16(1)
        append32(UInt32(rate)); append32(UInt32(rate * 2)); append16(2); append16(16)
        append("data"); append32(payload)
        for sample in mix {
            let clamped = Int16(max(-1, min(1, sample)) * Double(Int16.max))
            append16(UInt16(bitPattern: clamped))
        }
        return data
    }()
}


/// Plays the chime on the Mac when this computer finishes pairing.
@MainActor
enum PairingChime {
    private static var sound: NSSound?

    static func play() {
        if sound == nil { sound = NSSound(data: PairingChimeSound.wav) }
        sound?.stop()
        sound?.play()
    }
}

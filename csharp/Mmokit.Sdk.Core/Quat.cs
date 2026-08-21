namespace Mmokit.Sdk.Core
{
    /// <summary>
    /// A unit quaternion.
    ///
    /// Deliberately Mmokit's own type rather than System.Numerics.Quaternion:
    /// the SDK's primary consumer is Unity, where that name is ambiguous
    /// against UnityEngine.Quaternion and forces every consuming file to
    /// disambiguate. Converting to either is a four-field construction.
    /// </summary>
    public readonly struct Quat
    {
        public readonly float X;
        public readonly float Y;
        public readonly float Z;
        public readonly float W;

        public Quat(float x, float y, float z, float w)
        {
            X = x;
            Y = y;
            Z = z;
            W = w;
        }

        /// <summary>The zero rotation.</summary>
        public static readonly Quat Identity = new Quat(0f, 0f, 0f, 1f);

        /// <summary>Returns this quaternion scaled to unit length; a zero-norm
        /// quaternion becomes identity, matching the Go and TypeScript
        /// references.</summary>
        public Quat Normalized()
        {
            double n = System.Math.Sqrt((double)X * X + (double)Y * Y + (double)Z * Z + (double)W * W);
            if (n == 0) return Identity;
            return new Quat((float)(X / n), (float)(Y / n), (float)(Z / n), (float)(W / n));
        }

        public override string ToString() => $"({X}, {Y}, {Z}, {W})";
    }
}

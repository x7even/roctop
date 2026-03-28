import argparse
from roctop.app import RoctopApp


def main() -> None:
    parser = argparse.ArgumentParser(description="Terminal UI for AMD GPU monitoring via ROCm")
    parser.add_argument(
        "--refresh", type=float, default=2.0,
        metavar="SECONDS", help="Refresh interval in seconds (default: 2.0)"
    )
    args = parser.parse_args()
    app = RoctopApp(refresh_interval=args.refresh)
    app.run()


if __name__ == "__main__":
    main()
